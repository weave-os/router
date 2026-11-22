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
	"weave-os/router/internal/routingpolicy"
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

func populatedHandoffRoute(t *testing.T) routingpolicy.TurnRoute {
	t.Helper()
	var route routingpolicy.TurnRoute
	unfilled := fillExported(reflect.ValueOf(&route).Elem(), "route", map[reflect.Type]bool{})
	require.Empty(t, unfilled, "every exported route field must carry a value so the fixture pins its name and position")
	return route
}

func fillExported(v reflect.Value, path string, onPath map[reflect.Type]bool) []string {
	if v.Type() == reflect.TypeFor[time.Time]() {
		v.Set(reflect.ValueOf(piHandoffFixtureMinted.Add(time.Duration(len(path)) * time.Second)))
		return nil
	}
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(len(path)%100) + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(len(path)%100) + 1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(len(path)%100) + 0.5)
	case reflect.String:
		v.SetString(path)
	case reflect.Array:
		var unfilled []string
		for i := range v.Len() {
			unfilled = append(unfilled, fillExported(v.Index(i), fmt.Sprintf("%s.%d", path, i), onPath)...)
		}
		return unfilled
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Func {
			return nil
		}
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		return fillExported(v.Index(0), path+".0", onPath)
	case reflect.Map:
		key, elem := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		unfilled := append(fillExported(key, path+".key", onPath), fillExported(elem, path+".value", onPath)...)
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(key, elem)
		return unfilled
	case reflect.Pointer:
		if onPath[v.Type()] {
			return nil
		}
		onPath[v.Type()] = true
		defer delete(onPath, v.Type())
		v.Set(reflect.New(v.Type().Elem()))
		return fillExported(v.Elem(), path, onPath)
	case reflect.Struct:
		var unfilled []string
		for i := range v.NumField() {
			if field := v.Type().Field(i); field.IsExported() {
				unfilled = append(unfilled, fillExported(v.Field(i), path+"."+field.Name, onPath)...)
			}
		}
		return unfilled
	case reflect.Interface:
		return []string{path}
	default:
		return []string{path}
	}
	return nil
}
