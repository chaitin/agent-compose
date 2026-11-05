package capproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestIntegrationRuntimeSDKCancellationReachesUpstream(t *testing.T) {
	if os.Getenv("AC_RUNNER_OB_TEST") != "1" {
		t.Skip("requires built Runtime SDK, Node and grpcurl")
	}
	started := make(chan struct{})
	cancelled := make(chan error, 1)
	upstream := startScalarSDKServer(t, func(ctx context.Context, _ *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		close(started)
		<-ctx.Done()
		cancelled <- ctx.Err()
		return nil, status.FromContextError(ctx.Err()).Err()
	})
	address, stop := startTestProxy(t, Config{Listen: "127.0.0.1:0", OctoBus: staticOctoBus(upstream, "upstream-only")}, &revocableSDKResolver{})
	t.Cleanup(stop)
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-started:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(ready.Close)
	script := fmt.Sprintf(`import {capabilities} from './runtime/agent-compose-runtime-sdk/dist/esm/index.js';
const controller=new AbortController();
const result=capabilities.call('a'.repeat(64),'wait-for-cancellation',{signal:controller.signal}).then(
  ()=>{throw Error('cancelled call returned success')},error=>error);
// Abort only after the real business handler starts, without a timing race.
await fetch(%q);
controller.abort();
const error=await result;
if(!error.message.includes('cancelled'))throw Error('cancellation not reported');
console.log('cancelled-sdk-call');`, ready.URL)
	if output := runScalarSDKProgram(t, address, script); !strings.Contains(output, "cancelled-sdk-call") {
		t.Fatal("SDK did not report cancellation")
	}
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("upstream cancellation = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream kept running after SDK cancellation")
	}
}
