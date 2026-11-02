"""Local adapter for the active agent_flow HDP-HMM sidecar bundle."""

from __future__ import annotations

import asyncio
import inspect
import json
import logging
import time
from collections.abc import Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

from hmm_sidecar.raw_v5.codebook import CachedLatentModel
from hmm_sidecar.raw_v5.pickle_compat import load_latent_model
from hmm_sidecar.raw_v5.af_embeddings import (
    EmbeddingVector,
    embed_texts,
    embed_texts_uncached,
)
from hmm_sidecar.raw_v5.conversation_sequence import (
    AGENT_ELEMENT,
    ELEMENT_KIND_SLOTS,
    USER_ELEMENT,
)
from hmm_sidecar.raw_v5.embedding_singleflight import EmbeddingSingleFlight
from hmm_sidecar.raw_v5.hmm_feature_arms import HMMChain, hmm_chain_of
from hmm_sidecar.raw_v5.interaction_state import (
    interaction_filter,
    round_observations,
    routing_state_features,
)
from hmm_sidecar.raw_v5.raw_v5_contract import RAW_V5_ROUTER_EMBEDDING_MODEL
from hmm_sidecar.raw_v5.schemas import HMMStateSummary
from hmm_sidecar.raw_v5.state_representation import (
    GEMINI_CANONICAL_V1,
    GEMINI_EMBEDDING_2_LAST_STATE_V1,
    GEMINI_EMBEDDING_2_TEN_TURN_V1,
    GEMINI_EMBEDDING_2_TWO_TURN_V1,
    GEMINI_EMBEDDING_REPRESENTATIONS,
    CONTEXTUAL_CANONICAL_REPRESENTATIONS,
    RAW_V5_ROUTER_REPRESENTATIONS,
    SequenceRepresentation,
    StateEncoderContract,
    VOYAGE_RAW_ROUTER_REPRESENTATIONS,
    get_state_encoder_contract,
    gemini_last_state_documents,
    gemini_ten_turn_documents,
    gemini_two_turn_documents,
    resolve_sequence_representation,
)
from hmm_sidecar.raw_v5.af_embeddings import DEFAULT_VERTEX_LOCATION

log = logging.getLogger(__name__)

DEFAULT_CURRENT_MODEL_PATH = Path("ml_dev/agent_flow/gold_models/current.json")
#: Documents per provider request. Shared by serving and offline callers, so it
#: carries no latency policy of its own — the HMM sidecar server enforces the
#: bound its own inference deadline needs (see ``resolve_embedding_batch_size``
#: in ``router-hmm-sidecar/server.py``).
DEFAULT_EMBEDDING_BATCH_SIZE = 8
#: Provider requests in flight per fetch. One keeps offline callers on the paced
#: sequential path they were tuned for; the sidecar server raises it so a cold
#: prefix costs one round trip rather than one per batch.
DEFAULT_EMBEDDING_REQUEST_CONCURRENCY = 1
DEFAULT_EMBEDDING_MAX_RETRIES = 6
EMBEDDING_CACHE_MAX_TEXTS = 8192
SUPPORTED_HMM_BASELINES = {"full_bayes_sticky_hdp_hmm", "sticky_hdp_hmm"}


class AgentFlowSidecarError(RuntimeError):
    """Raised when the active agent_flow sidecar cannot produce a state."""


def _posterior_to_gamma(posterior: Any) -> np.ndarray:
    raise NotImplementedError("not on the classifier serving path")


def _live_belief_dynamics(
    model: object,
    projected: np.ndarray,
) -> tuple[float, ...] | None:
    raise NotImplementedError("not on the classifier serving path")


@dataclass(frozen=True)
class InferTimings:
    """Wall-clock cost of each stage of one HMM inference, in milliseconds.

    Routing runs to completion before the upstream request is dispatched, so
    this time is prepended to the caller's wait for a first token. The stages
    are split because they have very different characters: ``embed_ms`` is a
    network round trip whose cost tracks how many documents missed the cache,
    while ``posterior_ms`` is local numpy work that tracks sequence length.
    Collapsed into one number, a regression in either is indistinguishable.

    ``embed_cache_misses`` is the one to watch. Every turn re-embeds the whole
    conversation, so a warm cache should leave only the newest document to
    fetch; a persistently high value means the cache is missing, and each
    process holds its own, so a session spread across replicas re-fetches
    documents another replica already has.

    Both counters describe the **process-local** cache specifically. When a
    caller configures ``embedding_cache_path``, a miss is handed to
    ``embed_texts``, which may still serve it from that on-disk cache without
    touching the provider — so a miss is "not already in memory", not
    "definitely a network call". Serving passes no cache path, which is why the
    two coincide there.

    ``embed_cache_evictions`` is the cache-thrashing signal: a FIFO evicting
    on every request under normal load looks identical to a healthy cache in
    every other field, so this counts it per-request instead of leaving it
    implicit in the cache-size vs. hit-rate relationship.

    Carries no text, ids, or model output — only durations and counts — so it
    is safe to log verbatim.
    """

    documents_ms: float
    embed_ms: float
    posterior_ms: float
    total_ms: float
    documents: int
    embed_cache_hits: int
    embed_cache_misses: int
    embed_inflight_joins: int
    embed_cache_evictions: int

    def log_fields(self) -> str:
        raise NotImplementedError("not on the classifier serving path")


@dataclass(frozen=True)
class AgentFlowHMMReadout:
    summary: HMMStateSummary
    bundle_version: str
    model_path: Path
    embedding_model: str
    sequence_contract: dict[str, Any]
    # Serving features for the v2 complexity classifier (route_selector).
    # All already in scope during ``ainfer_turns`` so route selection needs
    # no re-embed. ``prev_state`` follows the training convention: the
    # previous hard state, or the current state on the first turn.
    latest_embedding: tuple[float, ...] = ()
    embedding_prefix: tuple[tuple[float, ...], ...] = ()
    gamma_row: tuple[float, ...] = ()
    prev_state: int = 0
    prefix_length: int = 0
    # User rounds before the open one, within the live window.
    completed_rounds: int = 0
    # Causal belief dynamics over this prefix, or None when the loaded HMM
    # cannot filter. Only a classifier whose layout declares the block reads
    # them; see serving_features.belief_dynamics_features.
    belief_dynamics: tuple[float, ...] | None = None


@dataclass(frozen=True)
class LiveFlowTurn:
    """One live conversation timestep in the agent_flow sequence format."""

    turn_text: str
    categorical: tuple[float, ...] = ()


def completed_rounds(turns: Sequence[LiveFlowTurn]) -> int:
    user_slot = ELEMENT_KIND_SLOTS[USER_ELEMENT]
    user_elements = sum(
        1
        for turn in turns
        if len(turn.categorical) > user_slot and turn.categorical[user_slot] == 1.0
    )
    return max(user_elements - 1, 0)


class AgentFlowHMMSidecar:
    """Load and apply the active prod agent_flow HDP-HMM sidecar locally."""

    def __init__(
        self,
        *,
        workweave_dir: Path,
        current_model_path: Path | None = None,
        bundle_dir: Path | None = None,
        use_vertex: bool = True,
        vertex_project: str | None = None,
        vertex_location: str | None = None,
        vertex_model: str | None = None,
        vertex_output_dimensionality: int | None = 3072,
        vertex_task_type: str | None = None,
        embedding_batch_size: int = DEFAULT_EMBEDDING_BATCH_SIZE,
        embedding_request_concurrency: int = DEFAULT_EMBEDDING_REQUEST_CONCURRENCY,
        embedding_max_retries: int = DEFAULT_EMBEDDING_MAX_RETRIES,
        embedding_request_timeout_seconds: float | None = None,
        embedding_wait_for_network: bool = False,
        embedding_persist_vertex_client: bool = False,
        embedding_inflight_max_age_seconds: float | None = None,
        embedding_cache_path: Path | None = None,
        gemma_state_cache_path: Path | None = None,
        use_voyage_direct: bool = False,
        offload_posterior: bool = False,
    ) -> None:
        self.offload_posterior = offload_posterior
        self.workweave_dir = workweave_dir
        self.current_model_path = self._resolve_path(
            current_model_path or DEFAULT_CURRENT_MODEL_PATH
        )
        # Explicit pinned bundle dir (e.g. a conversation-sequence bundle from
        # train_conversation_hmm) bypasses the current.json pointer entirely.
        self._explicit_bundle_dir = (
            self._resolve_path(bundle_dir) if bundle_dir is not None else None
        )
        self.use_vertex = use_vertex
        self.vertex_project = vertex_project
        self.vertex_location = (
            (vertex_location or DEFAULT_VERTEX_LOCATION)
            if use_vertex
            else vertex_location
        )
        self.vertex_model = vertex_model
        self.vertex_output_dimensionality = vertex_output_dimensionality
        self.vertex_task_type = vertex_task_type
        if embedding_max_retries < 0:
            raise ValueError("embedding_max_retries must be non-negative")
        if embedding_batch_size < 1:
            # Not merely defensive: the downstream ``max(1, batch_size)`` would
            # accept 0 and quietly serve a batch size the caller never chose.
            raise ValueError("embedding_batch_size must be at least 1")
        if embedding_request_concurrency < 1:
            raise ValueError("embedding_request_concurrency must be at least 1")
        if (
            embedding_request_timeout_seconds is not None
            and embedding_request_timeout_seconds <= 0
        ):
            raise ValueError("embedding_request_timeout_seconds must be positive")
        self.embedding_batch_size = embedding_batch_size
        self.embedding_request_concurrency = embedding_request_concurrency
        self.embedding_max_retries = embedding_max_retries
        self.embedding_request_timeout_seconds = embedding_request_timeout_seconds
        self.embedding_wait_for_network = embedding_wait_for_network
        # Only set True by callers that guarantee every infer_turns() call runs
        # on the same persistent event loop for the process lifetime (the HMM
        # sidecar's server.py, via its async runner). Any caller
        # that runs its own event loop per call (offline scripts like
        # promote_prod.py, build_feedback_dataset.py, router_proxy.py) MUST
        # leave this False — see the _CLIENT_CACHE comment in
        # ml_dev/vertexai/embeddings.py for why sharing a genai.Client across
        # a per-call loop leaks a client + an uncollectable closed loop,
        # unboundedly, per call.
        self.embedding_persist_vertex_client = embedding_persist_vertex_client
        self.embedding_cache_path = embedding_cache_path
        self.gemma_state_cache_path = gemma_state_cache_path
        # Raw-V5 Voyage embeddings go to Voyage's own API instead of the
        # OpenRouter broker. The vector contract is unchanged, so bundles and
        # embedding caches stay interchangeable across the two transports.
        if use_voyage_direct and use_vertex:
            raise ValueError("use_voyage_direct requires use_vertex=False")
        self.use_voyage_direct = use_voyage_direct

        self._current: dict[str, Any] | None = None
        self._bundle: dict[str, Any] | None = None
        self._state_cards: dict[int, dict[str, Any]] | None = None
        self._latent_model: CachedLatentModel | None = None
        # Provider-input document cache. Windowed Gemini representations key
        # this by their deterministic marked document, while canonical Gemini
        # keys it by the current canonical document.
        self._embedding_cache: dict[str, EmbeddingVector] = {}
        # Cumulative, process-local counterpart to the per-request
        # embed_cache_hits/misses reported in InferTimings. A miss is exactly
        # one Vertex embedding request in this serving configuration (no disk
        # cache path), so misses_total is the fleet-fragmentation and
        # Vertex-quota signal the per-request numbers can't answer alone.
        self._embed_cache_hits_total = 0
        self._embed_cache_misses_total = 0
        self._embed_cache_evictions_total = 0
        self._embedding_single_flight = EmbeddingSingleFlight(
            max_inflight_age_seconds=embedding_inflight_max_age_seconds,
        )
        self._pinned_embedding_texts: set[str] = set()
        self._gemma_state_client: Any | None = None

        self.validate_embedding_call_contract()

    def _embedding_call_base_kwargs(self, model: str) -> dict[str, Any]:
        voyage_raw = model == RAW_V5_ROUTER_EMBEDDING_MODEL
        if voyage_raw and not self.use_voyage_direct:
            raise RuntimeError(
                "Raw-V5 Voyage embeddings require direct Voyage transport; "
                "OpenRouter is not supported for this path"
            )
        return {
            "model": model,
            "batch_size": self.embedding_batch_size,
            "use_vertex": self.use_vertex,
            "vertex_project": self.vertex_project,
            "vertex_location": self.vertex_location,
            "vertex_model": self.vertex_model,
            "vertex_output_dimensionality": self.vertex_output_dimensionality,
            "vertex_task_type": self.vertex_task_type,
            "max_retries": self.embedding_max_retries,
            "vertex_wait_for_network": self.embedding_wait_for_network,
            "vertex_persist_client": self.embedding_persist_vertex_client,
            "return_info": True,
            # Prod serving must fail loud on Vertex errors — silent
            # OpenRouter fallback masks root causes and confuses ops.
            "allow_provider_fallback": False,
            "enforce_openrouter_gemini_embedding_2_global_zdr_policy": (
                not self.use_vertex and not voyage_raw
            ),
            # Raw-V5 Voyage models must use their pinned physical provider.
            # This is deliberately selected from the model identity rather
            # than the bundle here: this helper also runs during constructor
            # signature validation, before every compatibility test has an
            # on-disk bundle to resolve.
            "enforce_openrouter_voyageai_embedding_policy": False,
            "use_voyage_direct": voyage_raw and self.use_voyage_direct,
        }

    def _uncached_embedding_call_kwargs(self, model: str) -> dict[str, Any]:
        """The exact kwargs the live ``embed_texts_uncached`` call uses."""
        kwargs = self._embedding_call_base_kwargs(model)
        # Only the uncached path: the replay-cache callers are offline and their
        # batches are paced on purpose.
        kwargs["vertex_request_concurrency"] = self.embedding_request_concurrency
        if self.embedding_request_timeout_seconds is not None:
            kwargs["vertex_request_timeout"] = self.embedding_request_timeout_seconds
        return kwargs

    def _cached_embedding_call_kwargs(
        self,
        model: str,
        cache_path: Path,
        *,
        progress_desc: str = "feedback target replay embeddings",
    ) -> dict[str, Any]:
        """The exact kwargs every replay-cache ``embed_texts`` call uses.

        ``progress_desc`` is the only per-call-site variation; everything else
        is one shape so the contract validation covers all cached callers.
        """
        kwargs = self._embedding_call_base_kwargs(model)
        kwargs.update(
            {
                "cache_path": cache_path,
                "progress_desc": progress_desc,
                "progress_leave": True,
                "compact_cache_vectors": True,
                "compact_cache_vector_typecode": "f",
                "filter_cache_to_requested_texts": True,
                "strict_provider_cache": True,
            }
        )
        return kwargs

    def validate_embedding_call_contract(self) -> None:
        """Fail fast if the embedding call shapes drift from the real callees.

        Binds the exact kwargs both live embedding paths use against the
        current ``embed_texts`` / ``embed_texts_uncached`` signatures, so a
        caller/callee drift (e.g. a research-branch merge dropping a
        parameter) fails sidecar construction — a failed rollout the old
        revision survives — instead of a TypeError on live /route traffic.
        ``Signature.bind`` checks names/arity only; nothing executes and no
        network is touched.
        """
        probe_model = "embedding-call-contract-probe"
        probe_cache_path = self.embedding_cache_path or Path(
            "embedding-call-contract-probe.jsonl"
        )
        for callee, call_kwargs in (
            (
                embed_texts_uncached,
                self._uncached_embedding_call_kwargs(probe_model),
            ),
            (
                embed_texts,
                self._cached_embedding_call_kwargs(probe_model, probe_cache_path),
            ),
        ):
            try:
                inspect.signature(callee).bind(["probe"], **call_kwargs)
            except TypeError as exc:
                raise AgentFlowSidecarError(
                    "embedding call contract violated for "
                    f"{getattr(callee, '__name__', callee)!r}: {exc}"
                ) from exc

    def infer(self, texts: list[str]) -> AgentFlowHMMReadout:
        raise NotImplementedError("not on the classifier serving path")

    def infer_turns(self, turns: list[LiveFlowTurn]) -> AgentFlowHMMReadout:
        raise NotImplementedError("not on the classifier serving path")

    async def ainfer_turns(self, turns: list[LiveFlowTurn]) -> AgentFlowHMMReadout:
        raise NotImplementedError("not on the classifier serving path")

    async def ainfer_turns_with_timings(
        self, turns: list[LiveFlowTurn]
    ) -> tuple[AgentFlowHMMReadout, InferTimings | None]:
        """Same as :meth:`ainfer_turns`, plus the per-stage cost of producing it.

        Timings are returned rather than attached to
        :class:`AgentFlowHMMReadout` so the readout stays a pure model output
        that every other caller can keep treating as such.

        The timings are ``None`` when the path taken has no embed/posterior
        split to report. Returning zeros instead would log ``embed_ms=0.0``
        alongside ``embed_cache_misses=0``, which reads as a perfectly warm
        cache rather than as an unmeasured path.
        """
        started = time.perf_counter()
        clean_turns = [turn for turn in turns if turn.turn_text.strip()]
        if not clean_turns:
            raise AgentFlowSidecarError("cannot infer HMM state with empty live turns")
        representation = self.sequence_representation
        if representation in CONTEXTUAL_CANONICAL_REPRESENTATIONS:
            # The Gemma path resolves state through a separate service rather
            # than the embed-then-posterior pipeline, so it has no stage split
            # to report. The caller still times and logs the total.
            return await self._ainfer_gemma_turns(clean_turns), None
        if representation not in (
            *GEMINI_EMBEDDING_REPRESENTATIONS,
            *RAW_V5_ROUTER_REPRESENTATIONS,
        ):
            raise AgentFlowSidecarError(
                f"unsupported local HMM sequence representation: {representation.value}"
            )
        if representation in VOYAGE_RAW_ROUTER_REPRESENTATIONS and self.use_vertex:
            raise AgentFlowSidecarError(
                "raw V5 Voyage HMM serving requires use_vertex=False"
            )
        if representation in RAW_V5_ROUTER_REPRESENTATIONS and len(clean_turns) != len(
            turns
        ):
            # Raw-V5 documents are the actual pinned provider inputs. Dropping
            # one would change the causal HMM prefix (and potentially the U/A
            # alignment) instead of merely omitting cosmetic route context.
            raise AgentFlowSidecarError(
                "raw V5 HMM serving requires every pinned document to "
                "be non-empty; refusing to truncate the raw sequence"
            )
        embedding_model = self.embedding_model
        texts = [turn.turn_text for turn in clean_turns]
        documents_started = time.perf_counter()
        if representation is GEMINI_EMBEDDING_2_LAST_STATE_V1:
            documents = list(gemini_last_state_documents(texts))
        elif representation in (
            GEMINI_EMBEDDING_2_TWO_TURN_V1,
            GEMINI_EMBEDDING_2_TEN_TURN_V1,
        ):
            # These representations infer each element's user/agent role from
            # its POSITION in `texts` (no explicit kinds passed). That is only
            # correct if `texts` is exactly the original alternating sequence.
            # A blank turn was already dropped from `clean_turns` above; if
            # one fell in the interior, every later role shifts by one slot
            # and silently reassigns to the wrong actual turn. Fail closed
            # instead of building a mislabeled Round-window document.
            if len(clean_turns) != len(turns):
                raise AgentFlowSidecarError(
                    "Gemini turn-window representations require every live "
                    "turn to carry non-empty text; a blank turn would "
                    "silently shift user/agent roles"
                )
            documents = list(
                gemini_two_turn_documents(texts)
                if representation is GEMINI_EMBEDDING_2_TWO_TURN_V1
                else gemini_ten_turn_documents(texts)
            )
        else:
            # Raw-V5 documents (Voyage and Gemini alike) are constructed by the
            # explicit serving adapter before they enter this sidecar.
            # Re-wrapping them here would silently change the stored
            # model-input contract.
            documents = texts
        documents_ms = (time.perf_counter() - documents_started) * 1000.0
        embeddings = {
            document: self._embedding_cache[document]
            for document in dict.fromkeys(documents)
            if document in self._embedding_cache
        }
        missing = [
            document
            for document in dict.fromkeys(documents)
            if document not in embeddings
        ]
        unique_documents = len(dict.fromkeys(documents))
        embed_cache_hits = unique_documents - len(missing)
        self._embed_cache_hits_total += embed_cache_hits
        embedding_info = None
        embed_inflight_joins = 0
        embed_cache_misses = 0
        evictions = 0
        embed_started = time.perf_counter()
        if missing:

            async def fetch_missing(
                documents_to_fetch: list[str],
            ) -> tuple[dict[str, EmbeddingVector], Any]:
                # Counted at fetch initiation, not after resolve() returns: a
                # provider call that fails or is cancelled mid-flight still
                # consumed Vertex quota, and misses_total is documented as
                # the quota/cost signal. Single-flight hands this closure only
                # the owned documents, so joins never inflate it.
                self._embed_cache_misses_total += len(documents_to_fetch)
                if self.embedding_cache_path is not None:
                    return await embed_texts(
                        documents_to_fetch,
                        **self._cached_embedding_call_kwargs(
                            embedding_model,
                            self.embedding_cache_path,
                        ),
                    )
                return await embed_texts_uncached(
                    documents_to_fetch,
                    **self._uncached_embedding_call_kwargs(embedding_model),
                )

            single_flight = await self._embedding_single_flight.resolve(
                missing,
                fetch_missing,
            )
            new_embeddings = single_flight.embeddings
            embedding_info = single_flight.info
            embed_inflight_joins = single_flight.joined
            # fetched, not len(missing): a document joined to another caller's
            # in-flight fetch consumed no Vertex quota of its own (joins are
            # reported separately as embed_inflight_joins). The cumulative
            # misses_total advanced inside fetch_missing at fetch initiation,
            # so a failed provider call still counts there.
            embed_cache_misses = single_flight.fetched
            embeddings.update(new_embeddings)
            self._embedding_cache.update(new_embeddings)
            while len(self._embedding_cache) > EMBEDDING_CACHE_MAX_TEXTS:
                eviction_candidate = next(
                    (
                        document
                        for document in self._embedding_cache
                        if document not in self._pinned_embedding_texts
                    ),
                    None,
                )
                if eviction_candidate is None:
                    break
                self._embedding_cache.pop(eviction_candidate)
                evictions += 1
            # Fold into the cumulative counter once, after the loop, so the
            # per-request delta returned below and the running total can never
            # drift apart from a mid-loop miscount.
            self._embed_cache_evictions_total += evictions
        embedding_prefix = [embeddings[document] for document in documents]
        embed_ms = (time.perf_counter() - embed_started) * 1000.0
        posterior_started = time.perf_counter()
        readout = await self._ainfer_state_vectors(
            clean_turns,
            embedding_prefix,
            state_encoder_model=embedding_model,
            provider=(
                embedding_info.provider_used if embedding_info is not None else "cache"
            ),
            resolved_model=(
                embedding_info.resolved_model or embedding_info.cache_model
                if embedding_info is not None
                else embedding_model
            ),
        )
        posterior_ms = (time.perf_counter() - posterior_started) * 1000.0
        timings = InferTimings(
            documents_ms=documents_ms,
            embed_ms=embed_ms,
            posterior_ms=posterior_ms,
            total_ms=(time.perf_counter() - started) * 1000.0,
            documents=unique_documents,
            embed_cache_hits=embed_cache_hits,
            embed_cache_misses=embed_cache_misses,
            embed_inflight_joins=embed_inflight_joins,
            embed_cache_evictions=evictions,
        )
        return readout, timings

    async def _ainfer_gemma_turns(
        self,
        turns: list[LiveFlowTurn],
    ) -> AgentFlowHMMReadout:
        raise NotImplementedError("not on the classifier serving path")

    async def _ainfer_state_vectors(
        self,
        turns: list[LiveFlowTurn],
        state_vectors: Sequence[Sequence[float]],
        **kwargs: Any,
    ) -> AgentFlowHMMReadout:
        if getattr(self, "offload_posterior", False):
            # The HTTP service retains the enclosing inference task through
            # deadline/cancellation; native work must not occupy its probe loop.
            return await asyncio.to_thread(
                self.infer_state_vectors, turns, state_vectors, **kwargs
            )
        return self.infer_state_vectors(turns, state_vectors, **kwargs)

    def infer_state_vectors(
        self,
        turns: list[LiveFlowTurn],
        state_vectors: Sequence[Sequence[float]],
        *,
        state_encoder_model: str,
        provider: str = "external",
        resolved_model: str | None = None,
    ) -> AgentFlowHMMReadout:
        """Run the pinned projector/HMM over ordered boundary state vectors.

        Gemini serving obtains one vector per canonical document (or one
        previous+current pair document for Gemini Last State) in
        :meth:`ainfer_turns`. Gemma serving obtains one contextual vector per
        canonical U/A boundary from the dedicated Modal encoder. Keeping state
        acquisition outside this method prevents text-keyed caching from
        collapsing two identical boundary texts whose contextual Gemma states
        are different.
        """
        if not turns:
            raise AgentFlowSidecarError("cannot infer HMM state with no live turns")
        if len(turns) != len(state_vectors):
            raise AgentFlowSidecarError(
                "live turn/vector count mismatch: "
                f"got {len(turns)} turns and {len(state_vectors)} vectors"
            )
        if any(not turn.turn_text.strip() for turn in turns):
            raise AgentFlowSidecarError(
                "state-vector inference requires non-empty boundary documents"
            )
        latent_model = self.latent_model
        sequence_contract = self.sequence_contract
        if (
            hmm_chain_of(getattr(latent_model, "model_info", None))
            is HMMChain.INTERACTION_STATE
        ):
            return self._interaction_state_readout(
                turns,
                state_vectors,
                state_encoder_model=state_encoder_model,
            )
        raw = self._raw_feature_matrix(turns, state_vectors)
        expected_dim = int(sequence_contract.get("feature_dim") or 0)
        if expected_dim and raw.shape[1] != expected_dim:
            raise AgentFlowSidecarError(
                "live feature dimension does not match active sidecar contract: "
                f"got {raw.shape[1]}, expected {expected_dim}"
            )
        projected = latent_model.projector.transform(raw)
        belief_dynamics = _live_belief_dynamics(latent_model.model, projected)
        posterior = latent_model.model.posterior(projected)
        gamma = _posterior_to_gamma(posterior)
        if gamma.ndim != 2 or gamma.shape[0] == 0:
            raise AgentFlowSidecarError("active sidecar posterior returned no states")
        latest = gamma[-1]
        order = np.argsort(latest)[::-1]
        state_id = int(order[0])
        posterior_top = float(latest[state_id])
        posterior_margin = (
            float(latest[order[0]] - latest[order[1]])
            if len(order) > 1
            else posterior_top
        )
        hard_path = tuple(int(value) for value in np.argmax(gamma, axis=1).tolist())
        prev_state = int(hard_path[-2]) if len(hard_path) > 1 else state_id
        transition_key = None
        if len(hard_path) >= 2:
            transition_key = f"{hard_path[-2]}->{hard_path[-1]}"
        card = self.state_cards.get(state_id, {})
        summary = HMMStateSummary(
            state_id=state_id,
            state_label=str(card.get("name") or f"state_{state_id}"),
            state_description=str(card.get("short_description") or ""),
            posterior=posterior_top,
            posterior_margin=posterior_margin,
            state_path=hard_path,
            transition_key=transition_key,
        )
        log.debug(
            "agent_flow_hmm_infer bundle=%s state=%s posterior=%.4f margin=%.4f provider=%s state_encoder_model=%s",
            self.bundle_version,
            state_id,
            posterior_top,
            posterior_margin,
            provider,
            resolved_model or state_encoder_model,
        )
        embedding_prefix = tuple(
            tuple(float(value) for value in vector) for vector in state_vectors
        )
        return AgentFlowHMMReadout(
            summary=summary,
            bundle_version=self.bundle_version,
            model_path=self.latent_model_path,
            embedding_model=state_encoder_model,
            sequence_contract=sequence_contract,
            latest_embedding=embedding_prefix[-1],
            embedding_prefix=embedding_prefix,
            gamma_row=tuple(float(value) for value in latest),
            prev_state=prev_state,
            prefix_length=len(turns),
            completed_rounds=completed_rounds(turns),
            belief_dynamics=belief_dynamics,
        )

    def _interaction_state_readout(
        self,
        turns: list[LiveFlowTurn],
        state_vectors: Sequence[Sequence[float]],
        *,
        state_encoder_model: str,
    ) -> AgentFlowHMMReadout:
        """Round-level filter at the routed user boundary, as the dataset builder computes it.

        Each completed ``(U, A)`` round is one emission ``[P_user(U) | P_agent(A)]``;
        the routed round's agent block is unobserved, so its belief is
        p(z_t | o_{1:t-1}, U_t). The element-row matrix and its single PCA are
        never built for this chain.
        """
        latent_model = self.latent_model
        kinds = [
            (
                USER_ELEMENT
                if turn.categorical[ELEMENT_KIND_SLOTS[USER_ELEMENT]] == 1.0
                else AGENT_ELEMENT
            )
            for turn in turns
        ]
        if kinds[-1] != USER_ELEMENT:
            raise AgentFlowSidecarError(
                "interaction-state routing requires the sequence to end at a user boundary"
            )
        vectors = np.asarray(state_vectors, dtype=float)
        expected_state_dim = int(self.sequence_contract.get("state_dimension") or 0)
        if vectors.ndim != 2 or vectors.shape[1] != expected_state_dim:
            raise AgentFlowSidecarError(
                "state encoder vector width does not match active sidecar "
                f"contract: got {vectors.shape}, expected (*, {expected_state_dim})"
            )
        if not np.all(np.isfinite(vectors)):
            raise AgentFlowSidecarError("state encoder returned a non-finite vector")
        try:
            observations = round_observations(latent_model.projector, vectors, kinds)
            readout = interaction_filter(
                latent_model.model,
                observations,
                user_dim=latent_model.projector.user_dim,
            )
        except ValueError as error:
            raise AgentFlowSidecarError(str(error)) from error
        round_index = observations.shape[0] - 1
        routing = routing_state_features(readout, round_index)
        belief = routing.belief
        order = np.argsort(belief)[::-1]
        posterior_top = float(belief[routing.current_state])
        state_path = (
            *(
                int(np.argmax(completed))
                for completed in readout.completed_beliefs[:round_index]
            ),
            routing.current_state,
        )
        card = self.state_cards.get(routing.current_state, {})
        summary = HMMStateSummary(
            state_id=routing.current_state,
            state_label=str(card.get("name") or f"state_{routing.current_state}"),
            state_description=str(card.get("short_description") or ""),
            posterior=posterior_top,
            posterior_margin=(
                float(belief[order[0]] - belief[order[1]])
                if len(order) > 1
                else posterior_top
            ),
            state_path=state_path,
            transition_key=(
                f"{state_path[-2]}->{state_path[-1]}" if len(state_path) >= 2 else None
            ),
        )
        embedding_prefix = tuple(
            tuple(float(value) for value in vector) for vector in vectors
        )
        return AgentFlowHMMReadout(
            summary=summary,
            bundle_version=self.bundle_version,
            model_path=self.latent_model_path,
            embedding_model=state_encoder_model,
            sequence_contract=self.sequence_contract,
            latest_embedding=embedding_prefix[-1],
            embedding_prefix=embedding_prefix,
            gamma_row=tuple(float(value) for value in belief),
            prev_state=routing.previous_state,
            prefix_length=len(turns),
            completed_rounds=completed_rounds(turns),
            belief_dynamics=tuple(float(value) for value in routing.belief_dynamics),
        )

    @property
    def current(self) -> dict[str, Any]:
        raise NotImplementedError("not on the classifier serving path")

    @property
    def bundle_version(self) -> str:
        if self._explicit_bundle_dir is not None:
            value = self.bundle.get("bundle_version")
            return str(value) if value else self._explicit_bundle_dir.name
        value = self.current.get("current_sidecar_bundle_version") or self.current.get(
            "current_gold_model_version"
        )
        if not isinstance(value, str) or not value:
            raise AgentFlowSidecarError(
                f"current model file missing sidecar bundle version: {self.current_model_path}"
            )
        return value

    @property
    def bundle_dir(self) -> Path:
        if self._explicit_bundle_dir is not None:
            return self._explicit_bundle_dir
        return self.current_model_path.parent / self.bundle_version

    @property
    def bundle(self) -> dict[str, Any]:
        if self._bundle is None:
            self._bundle = _read_json_object(self.bundle_dir / "bundle.json")
        return self._bundle

    @property
    def sequence_contract(self) -> dict[str, Any]:
        contract = self.bundle.get("sequence_contract")
        if not isinstance(contract, dict):
            raise AgentFlowSidecarError(
                f"active bundle missing sequence_contract: {self.bundle_dir / 'bundle.json'}"
            )
        return contract

    @property
    def sequence_representation(self) -> SequenceRepresentation:
        raw = (
            self.bundle.get("sequence_representation")
            or self.sequence_contract.get("representation")
            or GEMINI_CANONICAL_V1.value
        )
        try:
            return resolve_sequence_representation(str(raw))
        except ValueError as exc:
            raise AgentFlowSidecarError(
                f"active bundle has an invalid sequence representation: {raw!r}"
            ) from exc

    def _uses_implicit_legacy_gemini_contract(self) -> bool:
        """Return whether this is the original pre-contract Gemini format.

        Gemini canonical bundles existed before state-encoder contracts were
        copied into bundle, sequence, and latent-model metadata.  Their
        sequence representation still identifies the encoder format, so later
        representations must not make those immutable artifacts unloadable.
        A partially populated contract is not legacy and remains invalid.
        """

        return (
            self.sequence_representation is GEMINI_CANONICAL_V1
            and self.bundle.get("state_encoder_contract") is None
            and self.sequence_contract.get("state_encoder_contract") is None
            and self.bundle.get("state_encoder_contract_sha256") is None
            and self.sequence_contract.get("state_encoder_contract_sha256") is None
        )

    @property
    def state_encoder_contract(self) -> StateEncoderContract:
        return get_state_encoder_contract(self.sequence_representation)

    @property
    def state_service_contract(self) -> dict[str, object] | None:
        raise NotImplementedError("not on the classifier serving path")

    @property
    def state_service_contract_sha256(self) -> str | None:
        raise NotImplementedError("not on the classifier serving path")

    @property
    def embedding_model(self) -> str:
        value = self.bundle.get("embedding_model") or self.sequence_contract.get(
            "embedding_model"
        )
        if not value:
            state_encoder = self.sequence_contract.get("state_encoder")
            if isinstance(state_encoder, dict):
                value = state_encoder.get("model")
        if not isinstance(value, str) or not value:
            raise AgentFlowSidecarError("active bundle missing state encoder model")
        if (
            self.sequence_representation in RAW_V5_ROUTER_REPRESENTATIONS
            and value != self.state_encoder_contract.model_id
        ):
            raise AgentFlowSidecarError(
                "raw V5 bundle embedding model does not match its "
                "state encoder contract"
            )
        return value

    @property
    def latent_model_path(self) -> Path:
        latent = self.bundle.get("latent_model")
        if not isinstance(latent, dict):
            raise AgentFlowSidecarError("active bundle missing latent_model block")
        raw = latent.get("model_path")
        if not isinstance(raw, str) or not raw:
            raise AgentFlowSidecarError("active bundle missing latent model path")
        return self.bundle_dir / raw

    @property
    def latent_model(self) -> CachedLatentModel:
        if self._latent_model is None:
            with self.latent_model_path.open("rb") as handle:
                payload = load_latent_model(handle)
            if not isinstance(payload, CachedLatentModel):
                raise AgentFlowSidecarError(
                    f"invalid active sidecar latent model: {self.latent_model_path}"
                )
            if payload.baseline not in SUPPORTED_HMM_BASELINES:
                raise AgentFlowSidecarError(
                    "active local sidecar must be one of "
                    f"{sorted(SUPPORTED_HMM_BASELINES)}, "
                    f"got {payload.baseline!r}"
                )
            if self.sequence_representation in (
                *GEMINI_EMBEDDING_REPRESENTATIONS,
                *RAW_V5_ROUTER_REPRESENTATIONS,
            ):
                provider_name = (
                    "Voyage"
                    if self.sequence_representation in VOYAGE_RAW_ROUTER_REPRESENTATIONS
                    else "Gemini"
                )
                latent_contract_sha256 = payload.model_info.get(
                    "state_encoder_contract_sha256"
                )
                expected_contract_sha256 = self.state_encoder_contract.sha256
                legacy_implicit_contract = self._uses_implicit_legacy_gemini_contract()
                if (
                    legacy_implicit_contract
                    and latent_contract_sha256 not in (None, expected_contract_sha256)
                ) or (
                    not legacy_implicit_contract
                    and latent_contract_sha256 != expected_contract_sha256
                ):
                    raise AgentFlowSidecarError(
                        f"{provider_name} latent model state encoder contract does not "
                        "match its bundle"
                    )
            elif self.sequence_representation in CONTEXTUAL_CANONICAL_REPRESENTATIONS:
                if (
                    payload.model_info.get("state_encoder_contract_sha256")
                    != self.state_encoder_contract.sha256
                    or payload.model_info.get("state_service_contract_sha256")
                    != self.state_service_contract_sha256
                    or payload.model_info.get("state_service_contract")
                    != self.state_service_contract
                ):
                    raise AgentFlowSidecarError(
                        "Gemma latent model state service contract does not match "
                        "its bundle"
                    )
            self._latent_model = payload
        return self._latent_model

    @property
    def state_cards(self) -> dict[int, dict[str, Any]]:
        if self._state_cards is None:
            cards_path = self.bundle_dir / "cards" / "state_cards.json"
            payload = json.loads(cards_path.read_text(encoding="utf-8"))
            if not isinstance(payload, list):
                raise AgentFlowSidecarError(f"invalid state cards: {cards_path}")
            cards: dict[int, dict[str, Any]] = {}
            for item in payload:
                if isinstance(item, dict) and isinstance(item.get("state_id"), int):
                    cards[int(item["state_id"])] = item
            self._state_cards = cards
        return self._state_cards

    def _raw_feature_matrix(
        self,
        turns: list[LiveFlowTurn],
        state_vectors: Sequence[Sequence[float]],
    ) -> np.ndarray:
        raise NotImplementedError("not on the classifier serving path")

    def _resolve_path(self, path: Path) -> Path:
        if path.is_absolute():
            return path
        return self.workweave_dir / path


def _read_json_object(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise AgentFlowSidecarError(f"expected JSON object: {path}")
    return payload
