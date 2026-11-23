package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"weave-os/router/internal/flags"
)

type fixtureSchema string
type fixtureCapability string
type escalationEvidenceSchema string

const (
	bindingSchema        fixtureSchema     = "router_benchmark_fixture_binding.v1"
	servingRegistryState fixtureCapability = "serving_registry_state"
)

const escalationSchema escalationEvidenceSchema = "router_benchmark_fixture_escalation.v1"

const (
	escalationVariablePrefix   = "HARNESS_FIXTURE_ESCALATION_"
	escalationSelectVariable   = escalationVariablePrefix + "SELECT"
	escalationActiveVariable   = escalationVariablePrefix + "CLASSIFIER"
	escalationShadowVariable   = escalationVariablePrefix + "SHADOW"
	escalationCadenceVariable  = escalationVariablePrefix + "CADENCE"
	escalationEvidenceVariable = escalationVariablePrefix + "PATH"
	escalationSelectEnabled    = "1"
)

var escalationOptionVariables = []string{escalationActiveVariable, escalationShadowVariable, escalationCadenceVariable, escalationEvidenceVariable}

type escalationRequest struct {
	Active  flags.EscalationClassifier
	Shadow  flags.EscalationClassifier
	Cadence int
}

type fixtureEscalation struct {
	SchemaVersion  escalationEvidenceSchema   `json:"schema_version"`
	InstallationID string                     `json:"installation_id"`
	Active         flags.EscalationClassifier `json:"active"`
	Shadow         flags.EscalationClassifier `json:"shadow"`
	Cadence        int                        `json:"cadence"`
	PreviousEpoch  int                        `json:"previous_epoch"`
	Epoch          int                        `json:"epoch"`
}

type fixtureBinding struct {
	SchemaVersion           fixtureSchema       `json:"schema_version"`
	RouterKey               string              `json:"router_key"`
	InstallationID          string              `json:"installation_id"`
	APIKeyID                string              `json:"api_key_id"`
	ManagedAdmissionReady   bool                `json:"managed_admission_ready"`
	UnavailableCapabilities []fixtureCapability `json:"unavailable_capabilities"`
	SubjectID               string              `json:"subject_id,omitempty"`
	BillingOverrideReady    bool                `json:"billing_override_ready"`
	PrepaidReady            bool                `json:"prepaid_ready"`
	OrganizationID          string              `json:"organization_id"`
}

var installationPattern = regexp.MustCompile(`Created installation ([0-9a-f-]{36})`)
var keyIDPattern = regexp.MustCompile(`Created API key\s+([0-9a-f-]{36})`)
var keyPattern = regexp.MustCompile(`\brk_[A-Za-z0-9_-]{20,}`)

type fixtureRequest struct {
	database          *url.URL
	output            string
	escalation        escalationRequest
	escalationPath    string
	escalationEnabled bool
}

func run() error {
	request, err := fixtureRequestFromEnv()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	installationID, externalID, err := runCanonicalSeed(ctx, request.database)
	if err != nil {
		return err
	}
	fixture, err := provisionManagedFixture(ctx, request.database.String(), installationID, externalID)
	if err != nil {
		return err
	}
	if request.escalationEnabled {
		evidence, err := selectFixtureEscalation(ctx, request.database.String(), installationID, request.escalation)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(evidence)
		if err != nil {
			return err
		}
		if err := writeFreshFile(request.escalationPath, encoded); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(completeFixtureBinding(fixture, installationID, externalID))
	if err != nil {
		return err
	}
	return writeFreshFile(request.output, payload)
}

func fixtureRequestFromEnv() (fixtureRequest, error) {
	if os.Getenv("HARNESS_FIXTURE_DISPOSABLE") != "1" {
		return fixtureRequest{}, errors.New("fixture setup requires an owned disposable database")
	}
	database, err := fixtureDatabaseFromEnv()
	if err != nil {
		return fixtureRequest{}, err
	}
	output := os.Getenv("HARNESS_FIXTURE_BINDING_PATH")
	if output == "" {
		output = "/state/fixture-binding.json"
	}
	if !filepath.IsAbs(output) {
		return fixtureRequest{}, errors.New("fixture binding path must be absolute")
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		return fixtureRequest{}, errors.New("fixture binding already exists; fresh cell state is required")
	}
	if err := rejectUnknownEscalationVariables(os.Environ()); err != nil {
		return fixtureRequest{}, err
	}
	requestedEscalation, escalationPath, escalationEnabled, err := escalationRequestFromEnv(os.LookupEnv)
	if err != nil {
		return fixtureRequest{}, err
	}
	if escalationEnabled {
		if err := requireFreshEscalationPath(escalationPath, output); err != nil {
			return fixtureRequest{}, err
		}
	}
	return fixtureRequest{database: database, output: output, escalation: requestedEscalation, escalationPath: escalationPath, escalationEnabled: escalationEnabled}, nil
}

func fixtureDatabaseFromEnv() (*url.URL, error) {
	database, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil || database.Scheme != "postgres" && database.Scheme != "postgresql" {
		return nil, errors.New("fixture setup requires a PostgreSQL URL")
	}
	expectedHost := os.Getenv("HARNESS_FIXTURE_DB_HOST")
	if expectedHost == "" || database.Hostname() != expectedHost || strings.Contains(expectedHost, ".") || strings.Contains(expectedHost, "/") {
		return nil, errors.New("fixture database must use its declared private service hostname")
	}
	if database.Query().Get("host") != "" || database.Query().Get("hostaddr") != "" || os.Getenv("PGSERVICE") != "" {
		return nil, errors.New("fixture database cannot override its private service hostname")
	}
	return database, nil
}

func requireFreshEscalationPath(escalationPath, output string) error {
	if filepath.Clean(escalationPath) == filepath.Clean(output) {
		return errors.New("fixture escalation evidence must not replace the fixture binding")
	}
	if _, err := os.Lstat(escalationPath); !os.IsNotExist(err) {
		return errors.New("fixture escalation evidence already exists; fresh cell state is required")
	}
	return nil
}

func runCanonicalSeed(ctx context.Context, database *url.URL) (string, string, error) {
	migrationQuery := database.Query()
	migrationQuery.Set("search_path", "router")
	database.RawQuery = migrationQuery.Encode()
	migration := exec.CommandContext(ctx, "/migrate", "-path", "/migrations", "-database", database.String(), "up")
	if _, err := migration.CombinedOutput(); err != nil {
		return "", "", errors.New("canonical migrations failed; component startup refused")
	}
	command := exec.CommandContext(ctx, "/seed")
	externalID := uuid.NewString()
	command.Env = append(os.Environ(), "SEED_EXTERNAL_ID="+externalID)
	seedOutput, err := command.Output()
	if err != nil {
		return "", "", errors.New("canonical seed failed; seed output withheld")
	}
	installation := installationPattern.FindSubmatch(seedOutput)
	keyID := keyIDPattern.FindSubmatch(seedOutput)
	keys := keyPattern.FindAll(seedOutput, -1)
	unique := make(map[string]struct{})
	for _, key := range keys {
		unique[string(key)] = struct{}{}
	}
	if len(installation) != 2 || len(keyID) != 2 || len(unique) != 1 {
		return "", "", errors.New("canonical seed output contract changed; output withheld")
	}
	return string(installation[1]), externalID, nil
}

func completeFixtureBinding(fixture fixtureBinding, installationID, externalID string) fixtureBinding {
	fixture.InstallationID = installationID
	fixture.OrganizationID = externalID
	fixture.PrepaidReady = true
	if !fixture.ManagedAdmissionReady {
		fixture.UnavailableCapabilities = []fixtureCapability{servingRegistryState}
	} else {
		fixture.UnavailableCapabilities = []fixtureCapability{}
	}
	fixture.SchemaVersion = bindingSchema
	return fixture
}

func writeFreshFile(path string, payload []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := bytes.NewReader(payload).WriteTo(file); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
