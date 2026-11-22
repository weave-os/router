package proxy

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/requestcontext"
)

var updatePiHandoffFixture = flag.Bool("update-pi-handoff", false, "rewrite the signed Pi handoff route fixture from current code")

var (
	piHandoffFixturePath   = filepath.Join("testdata", "pi_handoff", "route_v1.jwt")
	piHandoffFixtureSecret = []byte("synthetic-pi-handoff-fixture-secret-32b")
	piHandoffFixtureMinted = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

func TestPiHandoffRouteTokenFormatIsStable(t *testing.T) {
	route := populatedHandoffRoute(t)
	svc := &Service{piHandoffSecret: piHandoffFixtureSecret, clock: func() time.Time { return piHandoffFixtureMinted }}
	claims := handoffClaims{
		Purpose: handoffContinuation, APIKeyID: "fixture-key", SessionID: "fixture-session",
		RequestedModel: "fixture-model", Tools: "fixture-tools", Reasoning: "fixture-reasoning", Route: route,
	}

	if *updatePiHandoffFixture {
		token, err := svc.mintHandoff(claims)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(piHandoffFixturePath, []byte(token+"\n"), 0o600))
	}
	raw, err := os.ReadFile(piHandoffFixturePath)
	require.NoError(t, err)
	token := strings.TrimSpace(string(raw))

	ctx := context.WithValue(context.Background(), requestcontext.APIKeyIDContextKey{}, claims.APIKeyID)
	ctx = context.WithValue(ctx, requestcontext.InstallationIDContextKey{}, route.InstallationID.String())
	parsed, _, err := svc.parseHandoff(ctx, []byte(`{"`+piHandoffField+`":"`+token+`"}`))
	require.NoError(t, err)
	decoded := handoffFromContext(parsed)
	require.NotNil(t, decoded)

	assert.Equal(t, route, decoded.Route, "a token signed before the change must decode to the same route values")
	resigned, err := jwt.NewWithClaims(jwt.SigningMethodHS256, *decoded).SignedString(piHandoffFixtureSecret)
	require.NoError(t, err)
	assert.Equal(t, token, resigned, "re-signing the decoded claims must reproduce the fixture token byte for byte")
	minted, err := svc.mintHandoff(claims)
	require.NoError(t, err)
	assert.Equal(t, token, minted, "current code must mint the fixture token byte for byte")
}

func populatedHandoffRoute(t *testing.T) turnLoopResult {
	t.Helper()
	var route turnLoopResult
	fillExported(reflect.ValueOf(&route).Elem(), "route", 0)
	return route
}

func fillExported(v reflect.Value, path string, depth int) {
	if depth > 8 {
		return
	}
	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(piHandoffFixtureMinted.Add(time.Duration(len(path)) * time.Second)))
		return
	}
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(len(path) % 100))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(len(path) % 100))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(len(path)%100) + 0.5)
	case reflect.String:
		v.SetString(path)
	case reflect.Array:
		for i := range v.Len() {
			fillExported(v.Index(i), fmt.Sprintf("%s.%d", path, i), depth+1)
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Func {
			return
		}
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillExported(v.Index(0), path+".0", depth+1)
	case reflect.Map:
		key, elem := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		fillExported(key, path+".key", depth+1)
		fillExported(elem, path+".value", depth+1)
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(key, elem)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillExported(v.Elem(), path, depth+1)
	case reflect.Struct:
		for i := range v.NumField() {
			if field := v.Type().Field(i); field.IsExported() {
				fillExported(v.Field(i), path+"."+field.Name, depth+1)
			}
		}
	default:
	}
}
