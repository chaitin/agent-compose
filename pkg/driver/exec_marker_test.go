package driver

import (
	"reflect"
	"testing"
)

func TestExecEnvMapWithMarkerCopiesAndOwnsMarker(t *testing.T) {
	env := map[string]string{"A": "1", guestExecMarkerEnv: "caller"}
	marked := execEnvMapWithMarker(env, "exec-1")
	if !reflect.DeepEqual(marked, map[string]string{"A": "1", guestExecMarkerEnv: "exec-1"}) {
		t.Fatalf("marked env = %#v", marked)
	}
	if env[guestExecMarkerEnv] != "caller" {
		t.Fatalf("caller env was mutated: %#v", env)
	}
	if got := execEnvMapWithMarker(nil, "exec-2"); !reflect.DeepEqual(got, map[string]string{guestExecMarkerEnv: "exec-2"}) {
		t.Fatalf("marked nil env = %#v", got)
	}
}

func TestMarkedExecTerminationCommandPassesMarkerAndEnvName(t *testing.T) {
	command := markedExecTerminationCommand(guestExecMarkerEnv, "exec-1")
	if len(command) != 6 || command[0] != "sh" || command[1] != "-c" || command[2] != markedExecTerminationScript {
		t.Fatalf("command = %#v", command)
	}
	if command[4] != "exec-1" || command[5] != guestExecMarkerEnv {
		t.Fatalf("command arguments = %#v, want marker then env name", command[3:])
	}
}

func TestMarkedExecLeaderTerminationCommandPassesMarkerAndEnvName(t *testing.T) {
	command := markedExecLeaderTerminationCommand(guestExecMarkerEnv, "exec-1")
	if len(command) != 6 || command[0] != "sh" || command[1] != "-c" || command[2] != markedExecLeaderTerminationScript {
		t.Fatalf("command = %#v", command)
	}
	if command[4] != "exec-1" || command[5] != guestExecMarkerEnv {
		t.Fatalf("command arguments = %#v, want marker then env name", command[3:])
	}
	// The leader script must target only the root of the marker tree, never the
	// whole tree: it must not reuse the full-tree termination script.
	if markedExecLeaderTerminationScript == markedExecTerminationScript {
		t.Fatal("leader termination script must differ from the full-tree script")
	}
}
