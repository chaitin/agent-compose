package echofn

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestEpochTimeAPI_EncodesNestedBsonDDateTimeAsFloat(t *testing.T) {
	testEpochTimeAPIEncodesNestedBsonDDateTimeAsFloat(t)
}

func testEpochTimeAPIEncodesNestedBsonDDateTimeAsFloat(t *testing.T) {
	t.Helper()
	api := newEpochTimeAPI()

	dt := bson.NewDateTimeFromTime(time.Unix(0, 0).UTC().Add(1234 * time.Millisecond))

	payload := bson.M{
		"outer": bson.D{{Key: "create_time", Value: dt}},
	}

	b, err := api.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := string(b)
	want := "\"create_time\":1.234"
	if !contains(got, want) {
		t.Fatalf("expected nested datetime encoded as float, got %s", got)
	}
}

func contains(s, substr string) bool {
	return len(substr) == 0 || (len(s) >= len(substr) && indexOf(s, substr) >= 0)
}

func indexOf(s, substr string) int {
	// small, allocation-free helper to avoid importing strings in this package
	n := len(substr)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == substr {
			return i
		}
	}
	return -1
}
