package sources

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/daidr/Ariadne/sdk"
)

func TestReferenceCredentialsFollowTheirConfiguredHost(t *testing.T) {
	registry := New()
	_, err := registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "service"}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) { return sdk.Capabilities{}, nil }, SecretsFor: func(settings map[string]any) (string, error) {
		address, _ := settings["address"].(string)
		parsed, err := url.ParseRequestURI(address)
		if err != nil {
			return "", err
		}
		if parsed.Host == "" {
			return "", errors.New("no host")
		}
		return parsed.Host, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	part := func(address string) Part { return Part{Kind: "service", Settings: map[string]any{"address": address}} }
	own := part("https://one.example.test/project")
	host := part("https://one.example.test/hosted")
	source := &Definition{Part: own, Hosting: &host}
	secrets := map[string]any{"token": "own", "hosting.token": "hosted", "reference.r.token": "reference", "reference.other.token": "other"}
	for _, test := range []struct {
		address, id string
		want        map[string]any
	}{
		{"https://one.example.test/reference", "", map[string]any{"token": "own"}},
		{"https://one.example.test/reference", "r", map[string]any{"token": "reference"}},
		{"https://two.example.test/reference", "", map[string]any{}},
		{"https://two.example.test/reference", "r", map[string]any{"token": "reference"}},
		{"invalid", "", map[string]any{}},
	} {
		actual := registry.ReferenceSecrets(source, Reference{Part: part(test.address), ID: test.id}, secrets)
		if !reflect.DeepEqual(actual, test.want) {
			t.Fatal(test.address, actual, test.want)
		}
	}
}
func TestSourceConnectionDoesNotHandOutReferenceSecretsAndClosesOnce(t *testing.T) {
	registry := New()
	var received map[string]any
	var closed atomic.Int32
	_, err := registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "test"}, Connect: func(_ context.Context, settings map[string]any) (sdk.Capabilities, error) {
		received = settings
		return sdk.Capabilities{Close: func() error { closed.Add(1); return nil }}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := registry.ConnectSource(t.Context(), Definition{Part: Part{Kind: "test", Settings: map[string]any{"path": "project"}}}, map[string]any{"token": "own", "hosting.token": "other-host", "reference.r.token": "reference"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(received, map[string]any{"path": "project", "token": "own"}) {
		t.Fatal(received)
	}
	caps.Close()
	caps.Close()
	if closed.Load() != 1 {
		t.Fatal("source released twice", closed.Load())
	}
}
func TestFailedOrCanceledConnectionReleasesReturnedResources(t *testing.T) {
	for _, fail := range []bool{false, true} {
		registry := New()
		ctx, cancel := context.WithCancel(t.Context())
		var closed atomic.Int32
		_, err := registry.Register(sdk.ContextProvider{Info: sdk.ContextProviderInfo{Kind: "test"}, Connect: func(context.Context, map[string]any) (sdk.Capabilities, error) {
			var err error
			if fail {
				err = errors.New("connection failed")
			} else {
				cancel()
			}
			return sdk.Capabilities{Close: func() error { closed.Add(1); return nil }}, err
		}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.ConnectSource(ctx, Definition{Part: Part{Kind: "test"}}, nil); err == nil {
			t.Fatal("failed connection accepted")
		}
		cancel()
		if closed.Load() != 1 {
			t.Fatal("failed connection retained resources", closed.Load())
		}
	}
}
