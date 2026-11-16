package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestGatewayTaskStepPagesAreStableAndTaskScoped(t *testing.T) {
	f, in := gatewayTestSetup(t, 1_000_000, 10000, 1_000_000)
	task, err := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err = f.repo.AdmitGatewayTask(t.Context(), taskTestFact(task, in, fmt.Sprintf("paged-step-%016d", i))); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	cursor := ""
	for {
		page, e := f.repo.GatewayTaskSteps(t.Context(), f.ws.ID, f.user.ID, task.ID, cursor, 2)
		if e != nil {
			t.Fatal(e)
		}
		for _, step := range page.Items {
			if seen[step.ID] {
				t.Fatal("duplicate step across pages")
			}
			seen[step.ID] = true
		}
		if !page.HasMore {
			if page.NextCursor != "" {
				t.Fatal("terminal cursor")
			}
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatal("lost historical steps", len(seen))
	}
	other, err := f.repo.CreateGatewayTask(t.Context(), f.ws.ID, f.user.ID, taskTestInput(t, f, in))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.repo.GatewayTaskSteps(t.Context(), f.ws.ID, f.user.ID, other.ID, cursor, 2); !errors.Is(err, ErrInvalid) {
		t.Fatal("task cursor escaped", err)
	}
	if _, err = f.repo.GatewayTaskSteps(t.Context(), f.ws.ID, f.user.ID, task.ID, "", 201); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded page accepted", err)
	}
}
