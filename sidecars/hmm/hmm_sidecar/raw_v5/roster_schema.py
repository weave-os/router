"""Stored HMM roster-schema vocabulary shared by publishing and serving."""

from __future__ import annotations

from enum import StrEnum


class RosterSchemaVersion(StrEnum):
    OLDEST = "hmm_router_cluster_roster_v2"
    LEGACY = "hmm_router_cluster_roster_v3"
    PREVIOUS = "hmm_router_cluster_roster_v4"
    AA = "hmm_router_cluster_roster_v5"
    AA_FIVE_CLUSTER = "hmm_router_cluster_roster_v5_5c"
    WMI = "hmm_router_cluster_roster_v6"
    WMI_FIVE_CLUSTER = "hmm_router_cluster_roster_v6_6c"
    INDICES = "hmm_router_cluster_roster_v7"
    INDICES_FIVE_CLUSTER = "hmm_router_cluster_roster_v7_5c"


__all__ = ["RosterSchemaVersion"]
