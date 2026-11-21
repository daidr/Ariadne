package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/daidr/Ariadne/internal/domainerr"
	"github.com/daidr/Ariadne/internal/eventstore"
	"github.com/daidr/Ariadne/internal/schema"
)

func TestRepositoryDeletionAndTurnStartAreSerialized(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		repo, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "race.sqlite")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"项目","coordinatorId":"crd_a"}`)}}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var deleteErr, turnErr error
		wg.Add(2)
		go func() { defer wg.Done(); <-start; deleteErr = repo.DeleteProject(t.Context(), "prj_a") }()
		go func() {
			defer wg.Done()
			<-start
			_, turnErr = repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "turn.started", ProjectID: "prj_a", Data: json.RawMessage(`{"conversationId":"crd_a","turnId":"turn_a","model":"model_a"}`)}})
		}()
		close(start)
		wg.Wait()
		var domain *domainerr.Error
		if deleteErr == nil {
			if !errors.As(turnErr, &domain) || domain.Key != "error.notFound" {
				t.Fatalf("deleted project accepted turn: %v", turnErr)
			}
		} else {
			if !errors.As(deleteErr, &domain) || domain.Key != "error.projectBusy" || turnErr != nil {
				t.Fatalf("busy deletion: %v / %v", deleteErr, turnErr)
			}
		}
		_ = repo.Close()
	}
}

func TestFailedUpgradeRollsBackSchemaAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade-failure.sqlite")
	repo, err := Open(t.Context(), Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.Append(t.Context(), []eventstore.NewEvent{{Type: "project.created", ProjectID: "prj_a", Data: json.RawMessage(`{"name":"必须保留","coordinatorId":"crd_a"}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Store.DB().ExecContext(t.Context(), `PRAGMA user_version = 66; INSERT INTO events (id,type,project_id,data,at) VALUES ('evt_bad','artifact.created','prj_a','{}',1)`); err != nil {
		t.Fatal(err)
	}
	_ = repo.Close()
	if reopened, err := Open(t.Context(), Options{Path: path}); err == nil {
		_ = reopened.Close()
		t.Fatal("invalid event upgraded without error")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var name string
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 66 {
		t.Fatal(version, err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT name FROM projects WHERE id = 'prj_a'").Scan(&name); err != nil || name != "必须保留" {
		t.Fatal(name, err)
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = "+encoded(schema.Version+1)); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(t.Context(), Options{Path: path}); err == nil {
		_ = reopened.Close()
		t.Fatal("newer database version was downgraded")
	}
}
