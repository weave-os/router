package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/jackc/pgx/v5/pgxpool"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/policyclient"
	"weave-os/router/internal/policyregistry"
	"weave-os/router/internal/postgres"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router/hmm/rosterdata"
	"weave-os/router/internal/router/hmm/selection"
	"weave-os/router/internal/router/taskdomain"
)

type taskDomainBinding struct {
	ReleaseFile   string            `json:"release_file"`
	ReleaseSHA256 string            `json:"release_sha256"`
	Endpoint      string            `json:"endpoint"`
	BearerEnv     string            `json:"bearer_env"`
	EvidenceFiles map[string]string `json:"evidence_files"`
}

type loadedTaskDomain struct {
	classifier taskdomain.Classifier
	release    taskdomain.Release
	evidence   map[string][]byte
}

type taskDomainRuntime struct {
	store    *postgres.TaskDomainRepo
	bindings map[string]loadedTaskDomain
}

type taskDomainMetadataKind string

const (
	taskDomainBindings         taskDomainMetadataKind = "bindings"
	taskDomainReleases         taskDomainMetadataKind = "releases"
	taskDomainEvidence         taskDomainMetadataKind = "evidence"
	maxTaskDomainMetadataBytes                        = 4 << 20
	taskDomainRegistryPrefix                          = "weave_registry/router_task_domain/"
)

type taskDomainMetadataReader struct {
	client *storage.Client
}

func loadTaskDomainRuntime(pool *pgxpool.Pool) (*taskDomainRuntime, error) {
	configPath := os.Getenv("ROUTER_TASK_DOMAIN_BINDINGS_PATH")
	expectedBindingsDigest := os.Getenv("ROUTER_TASK_DOMAIN_BINDINGS_SHA256")
	if configPath == "" {
		if expectedBindingsDigest != "" {
			return nil, errors.New("task binding digest requires a bindings path")
		}
		return nil, nil
	}
	if pool == nil {
		return nil, errors.New("task classification requires persistent router storage")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	reader := &taskDomainMetadataReader{}
	defer func() {
		if reader.client != nil {
			_ = reader.client.Close()
		}
	}()
	bindings, err := loadTaskDomainBindings(ctx, reader, configPath, expectedBindingsDigest)
	if err != nil {
		return nil, err
	}
	return &taskDomainRuntime{store: postgres.NewTaskDomainRepo(pool), bindings: bindings}, nil
}

func loadTaskDomainBindings(ctx context.Context, reader *taskDomainMetadataReader, configPath, expectedBindingsDigest string) (map[string]loadedTaskDomain, error) {
	payload, err := reader.read(ctx, configPath, expectedBindingsDigest, taskDomainBindings)
	if err != nil {
		return nil, err
	}
	var bindings []taskDomainBinding
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bindings); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || len(bindings) == 0 || len(bindings) > 16 {
		return nil, errors.New("invalid task classifier binding inventory")
	}
	loadedBindings := make(map[string]loadedTaskDomain)
	for _, binding := range bindings {
		if _, exists := loadedBindings[binding.ReleaseSHA256]; exists {
			return nil, errors.New("duplicate task classifier release")
		}
		payload, err := reader.read(ctx, binding.ReleaseFile, binding.ReleaseSHA256, taskDomainReleases)
		if err != nil {
			return nil, err
		}
		release, err := taskdomain.ParseRelease(payload, binding.ReleaseSHA256)
		if err != nil {
			return nil, err
		}
		classifier, err := policyclient.NewTaskDomainClassifier(binding.Endpoint, os.Getenv(binding.BearerEnv), binding.ReleaseSHA256, nil)
		if err != nil {
			return nil, err
		}
		loaded := loadedTaskDomain{classifier: classifier, release: release, evidence: make(map[string][]byte)}
		for _, digest := range release.Evidence {
			payload, err := reader.read(ctx, binding.EvidenceFiles[digest], digest, taskDomainEvidence)
			if err != nil {
				return nil, err
			}
			loaded.evidence[digest] = payload
		}
		loadedBindings[binding.ReleaseSHA256] = loaded
	}
	return loadedBindings, nil
}

func (r *taskDomainMetadataReader) read(ctx context.Context, path, expectedDigest string, kind taskDomainMetadataKind) ([]byte, error) {
	var file io.ReadCloser
	var err error
	if strings.Contains(path, "://") {
		name, generation, refErr := taskDomainGCSReference(path, expectedDigest, kind)
		if refErr != nil {
			return nil, refErr
		}
		if r.client == nil {
			r.client, err = storage.NewClient(ctx)
			if err != nil {
				return nil, fmt.Errorf("create task metadata storage client: %w", err)
			}
		}
		file, err = r.client.Bucket("weave_ml").Object(name).Generation(generation).NewReader(ctx)
	} else {
		file, err = os.Open(path)
	}
	if err != nil {
		return nil, fmt.Errorf("open task %s metadata: %w", kind, err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxTaskDomainMetadataBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read task %s metadata: %w", kind, err)
	}
	if len(payload) > maxTaskDomainMetadataBytes {
		return nil, errors.New("task configuration exceeds size limit")
	}
	if expectedDigest != "" && (!taskdomain.ValidDigest(expectedDigest) || rosterdata.SHA256Hex(payload) != expectedDigest) {
		return nil, fmt.Errorf("task %s metadata digest mismatch", kind)
	}
	return payload, nil
}

func taskDomainGCSReference(path, expectedDigest string, kind taskDomainMetadataKind) (string, int64, error) {
	ref, err := url.Parse(path)
	if err != nil || ref.Scheme != "gs" || ref.Host != "weave_ml" || ref.User != nil || ref.RawQuery != "" || ref.ForceQuery || ref.RawPath != "" {
		return "", 0, errors.New("invalid task metadata registry reference")
	}
	name := taskDomainRegistryPrefix + string(kind) + "/sha256/" + expectedDigest + ".json"
	if !taskdomain.ValidDigest(expectedDigest) || ref.Path != "/"+name {
		return "", 0, errors.New("task metadata reference must match its namespace and digest")
	}
	generation, err := strconv.ParseInt(ref.Fragment, 10, 64)
	if err != nil || generation <= 0 || strconv.FormatInt(generation, 10) != ref.Fragment {
		return "", 0, errors.New("task metadata reference requires a positive generation")
	}
	return name, generation, nil
}

func (r *taskDomainRuntime) bind(candidate policyregistry.Candidate) (taskdomain.Resolver, *selection.DomainEvidence, string, error) {
	ref, selected := candidate.AuxiliaryModels[taskdomain.AuxiliaryModel]
	if !selected {
		return nil, nil, "", nil
	}
	if r == nil {
		return nil, nil, "", errors.New("admitted task classifier release is not loaded")
	}
	loaded, exists := r.bindings[ref.SHA256]
	if !exists {
		return nil, nil, "", errors.New("admitted task classifier release is unavailable")
	}
	evidenceSHA := loaded.release.Evidence[candidate.Policy.SHA256]
	if evidenceSHA == "" {
		return &proxy.TaskDomainResolver{ReleaseSHA256: ref.SHA256}, nil, "", nil
	}
	evidence, err := selection.ParseDomainEvidence(loaded.evidence[evidenceSHA], candidate.Policy)
	if err != nil {
		return nil, nil, "", err
	}
	return &proxy.TaskDomainResolver{Store: r.store, Classifier: loaded.classifier, ReleaseSHA256: ref.SHA256, EvidenceSHA256: evidenceSHA}, evidence, evidenceSHA, nil
}

func (r *taskDomainRuntime) sweep(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := r.store.Sweep(sweepCtx)
			cancel()
			if err != nil {
				observability.FromContext(ctx).Error("Task profile sweep failed", "err", err)
			}
		}
	}
}
