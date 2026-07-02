package agentcompose

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fastschema/qjs"
	"github.com/samber/do/v2"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type LoaderHost interface {
	Log(ctx context.Context, message string, payload any) error
	PublishEvent(ctx context.Context, topic string, payloadJSON string) (TopicEventRecord, error)
	Agent(ctx context.Context, prompt string, request LoaderAgentRequest) (LoaderAgentResult, error)
	Command(ctx context.Context, request LoaderCommandRequest) (LoaderCommandResult, error)
	LLM(ctx context.Context, prompt string, request LoaderLLMRequest) (LoaderLLMResult, error)
	StateGet(ctx context.Context, key string) (string, bool, error)
	StateSet(ctx context.Context, key, valueJSON string) error
	StateDelete(ctx context.Context, key string) error
	CallSessionRPC(ctx context.Context, method, requestJSON string) (string, error)
}

type LoaderValidationResult struct {
	Triggers []LoaderTrigger
	Warnings []string
}

type LoaderExecutionRequest struct {
	Runtime     string
	Script      string
	Trigger     *LoaderTrigger
	PayloadJSON string
}

type LoaderExecutionResult struct {
	Triggers   []LoaderTrigger
	Warnings   []string
	ResultJSON string
}

type LoaderEngine interface {
	Validate(ctx context.Context, runtime, script string) (LoaderValidationResult, error)
	Execute(ctx context.Context, request LoaderExecutionRequest, host LoaderHost) (LoaderExecutionResult, error)
}

type QJSLoaderEngine struct{}

type loaderRegistration struct {
	trigger  LoaderTrigger
	callback *qjs.Value
	order    int
}

type loaderExecutionState struct {
	ctx           context.Context
	host          LoaderHost
	registrations []loaderRegistration
	seenIDs       map[string]struct{}
}

func NewLoaderEngine(do.Injector) (LoaderEngine, error) {
	return &QJSLoaderEngine{}, nil
}

func (e *QJSLoaderEngine) Validate(ctx context.Context, runtime, script string) (LoaderValidationResult, error) {
	result, err := e.execute(ctx, LoaderExecutionRequest{Runtime: runtime, Script: script}, nil, true)
	if err != nil {
		return LoaderValidationResult{}, err
	}
	return LoaderValidationResult{Triggers: result.Triggers, Warnings: result.Warnings}, nil
}

func (e *QJSLoaderEngine) Execute(ctx context.Context, request LoaderExecutionRequest, host LoaderHost) (LoaderExecutionResult, error) {
	return e.execute(ctx, request, host, false)
}

func (e *QJSLoaderEngine) execute(ctx context.Context, request LoaderExecutionRequest, host LoaderHost, validateOnly bool) (LoaderExecutionResult, error) {
	runtimeName, err := normalizeLoaderRuntime(request.Runtime)
	if err != nil {
		return LoaderExecutionResult{}, err
	}
	if runtimeName != LoaderRuntimeScheduler {
		return LoaderExecutionResult{}, fmt.Errorf("unsupported loader runtime %q", runtimeName)
	}
	if strings.TrimSpace(request.Script) == "" {
		return LoaderExecutionResult{}, fmt.Errorf("loader script is required")
	}

	rt, err := qjs.New(qjs.Option{
		Context:          ctx,
		MemoryLimit:      64 << 20,
		MaxExecutionTime: loaderEngineMaxExecutionTime(ctx),
	})
	if err != nil {
		return LoaderExecutionResult{}, fmt.Errorf("create qjs runtime: %w", err)
	}
	defer rt.Close()

	jsctx := rt.Context()
	state := &loaderExecutionState{
		ctx:           ctx,
		host:          host,
		registrations: make([]loaderRegistration, 0),
		seenIDs:       make(map[string]struct{}),
	}

	if _, err = e.installRuntime(jsctx, state); err != nil {
		return LoaderExecutionResult{}, err
	}

	evalResult, err := jsctx.Eval("loader.js", qjs.Code(request.Script), qjs.FlagAsync())
	if err != nil {
		state.freeCallbacks()
		return LoaderExecutionResult{}, fmt.Errorf("evaluate loader script: %w", err)
	}
	if evalResult != nil {
		if evalResult.IsPromise() {
			if _, err := evalResult.Await(); err != nil {
				state.freeCallbacks()
				return LoaderExecutionResult{}, fmt.Errorf("await loader script: %w", err)
			}
		}
	}

	warnings := make([]string, 0)
	if len(state.registrations) == 0 {
		mainFn := jsctx.Global().GetPropertyStr("main")
		hasMain := mainFn.IsFunction()
		if !hasMain {
			warnings = append(warnings, "script does not register any trigger and does not define main()")
		}
	}

	result := LoaderExecutionResult{
		Triggers: state.triggers(),
		Warnings: warnings,
	}
	if validateOnly {
		state.freeCallbacks()
		return result, nil
	}
	if host == nil {
		state.freeCallbacks()
		return LoaderExecutionResult{}, fmt.Errorf("loader host is required for execution")
	}

	payloadValue, err := payloadValueFromJSON(jsctx, request.PayloadJSON)
	if err != nil {
		state.freeCallbacks()
		return LoaderExecutionResult{}, err
	}

	executed, err := e.executeRequestedHandler(jsctx, state, request.Trigger, payloadValue)
	if err != nil {
		state.freeCallbacks()
		return LoaderExecutionResult{}, err
	}
	if executed != nil {
		if executed.IsPromise() {
			awaited, err := executed.Await()
			if err != nil {
				state.freeCallbacks()
				return LoaderExecutionResult{}, fmt.Errorf("await loader handler: %w", err)
			}
			executed = awaited
		}
		if jsonResult, ok, err := loaderResultJSON(executed); err != nil {
			state.freeCallbacks()
			return LoaderExecutionResult{}, err
		} else if ok {
			result.ResultJSON = jsonResult
		}
	}
	if err := ctx.Err(); err != nil {
		state.freeCallbacks()
		return LoaderExecutionResult{}, err
	}
	state.freeCallbacks()
	return result, nil
}

func loaderEngineMaxExecutionTime(ctx context.Context) int {
	const defaultMaxExecutionTimeMs = int((60 * time.Minute) / time.Millisecond)
	deadline, ok := ctx.Deadline()
	if !ok {
		return defaultMaxExecutionTimeMs
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return 1
	}
	remainingMs := int(remaining / time.Millisecond)
	if remainingMs < 1 {
		return 1
	}
	return remainingMs
}

func (e *QJSLoaderEngine) installRuntime(jsctx *qjs.Context, state *loaderExecutionState) (*qjs.Value, error) {
	schedulerObj := jsctx.NewObject()
	global := jsctx.Global()
	global.SetPropertyStr("scheduler", schedulerObj)
	if err := installLoaderSchemaBuilder(jsctx); err != nil {
		return nil, err
	}

	registerTimer := func(kind string, call *qjs.This) (*qjs.Value, error) {
		var (
			id       string
			delayMs  int64
			callback *qjs.Value
			autoID   bool
			err      error
			specJSON string
		)
		switch kind {
		case LoaderTriggerKindInterval:
			id, delayMs, callback, autoID, err = parseIntervalRegistration(call.Args())
			specJSON = fmt.Sprintf(`{"kind":"interval","intervalMs":%d}`, delayMs)
		case LoaderTriggerKindTimeout:
			id, delayMs, callback, autoID, err = parseTimeoutRegistration(call.Args())
			specJSON = fmt.Sprintf(`{"kind":"timeout","delayMs":%d}`, delayMs)
		default:
			return nil, fmt.Errorf("unsupported timer trigger kind %q", kind)
		}
		if err != nil {
			return nil, err
		}
		if id == "" {
			id = loaderTriggerStableID(kind, "", delayMs, callback.String(), len(state.registrations))
			autoID = true
		}
		trigger := LoaderTrigger{
			ID:         id,
			Kind:       kind,
			IntervalMs: delayMs,
			Enabled:    true,
			AutoID:     autoID,
			SpecJSON:   specJSON,
		}
		if err := state.register(trigger, callback); err != nil {
			return nil, err
		}
		return jsctx.NewString(id), nil
	}

	setIntervalFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		return registerTimer(LoaderTriggerKindInterval, call)
	})
	global.SetPropertyStr("setInterval", setIntervalFn)
	schedulerObj.SetPropertyStr("setInterval", setIntervalFn.Clone())
	schedulerObj.SetPropertyStr("interval", setIntervalFn.Clone())

	setTimeoutFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		return registerTimer(LoaderTriggerKindTimeout, call)
	})
	global.SetPropertyStr("setTimeout", setTimeoutFn)
	schedulerObj.SetPropertyStr("setTimeout", setTimeoutFn.Clone())
	schedulerObj.SetPropertyStr("timeout", setTimeoutFn.Clone())

	clearIntervalFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if len(call.Args()) > 0 {
			state.unregister(triggerHandle(call.Args()[0]), LoaderTriggerKindInterval)
		}
		return jsctx.NewUndefined(), nil
	})
	global.SetPropertyStr("clearInterval", clearIntervalFn)
	schedulerObj.SetPropertyStr("clearInterval", clearIntervalFn.Clone())

	clearTimeoutFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if len(call.Args()) > 0 {
			state.unregister(triggerHandle(call.Args()[0]), LoaderTriggerKindTimeout)
		}
		return jsctx.NewUndefined(), nil
	})
	global.SetPropertyStr("clearTimeout", clearTimeoutFn)
	schedulerObj.SetPropertyStr("clearTimeout", clearTimeoutFn.Clone())

	onFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		topic, id, callback, autoID, err := parseEventRegistration(call.Args())
		if err != nil {
			return nil, err
		}
		if id == "" {
			id = loaderTriggerStableID(LoaderTriggerKindEvent, topic, 0, callback.String(), len(state.registrations))
			autoID = true
		}
		trigger := LoaderTrigger{
			ID:       id,
			Kind:     LoaderTriggerKindEvent,
			Topic:    topic,
			Enabled:  true,
			AutoID:   autoID,
			SpecJSON: fmt.Sprintf(`{"kind":"event","topic":%q}`, topic),
		}
		if err := state.register(trigger, callback); err != nil {
			return nil, err
		}
		return jsctx.NewString(id), nil
	})
	schedulerObj.SetPropertyStr("on", onFn)
	schedulerObj.SetPropertyStr("addEventListener", onFn.Clone())

	cronFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		expr, id, callback, specJSON, autoID, err := parseCronRegistration(call.Args())
		if err != nil {
			return nil, err
		}
		if id == "" {
			id = loaderTriggerStableID(LoaderTriggerKindCron, expr, 0, callback.String(), len(state.registrations))
			autoID = true
		}
		trigger := LoaderTrigger{
			ID:       id,
			Kind:     LoaderTriggerKindCron,
			Enabled:  true,
			AutoID:   autoID,
			SpecJSON: specJSON,
		}
		if err := state.register(trigger, callback); err != nil {
			return nil, err
		}
		return jsctx.NewString(id), nil
	})
	schedulerObj.SetPropertyStr("cron", cronFn)
	schedulerObj.SetPropertyStr("schedule", cronFn.Clone())

	logFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if state.host == nil {
			return jsctx.NewUndefined(), nil
		}
		args := call.Args()
		if len(args) == 0 {
			return nil, fmt.Errorf("scheduler.log requires a message")
		}
		message := strings.TrimSpace(args[0].String())
		if message == "" {
			return nil, fmt.Errorf("scheduler.log requires a non-empty message")
		}
		var payload any
		if len(args) > 1 {
			value, err := qjs.ToGoValue[any](args[1])
			if err != nil {
				return nil, fmt.Errorf("decode scheduler.log payload: %w", err)
			}
			payload = value
		}
		if err := state.host.Log(state.ctx, message, payload); err != nil {
			return nil, err
		}
		return jsctx.NewUndefined(), nil
	})
	schedulerObj.SetPropertyStr("log", logFn)

	eventObj := jsctx.NewObject()
	eventPublishFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if state.host == nil {
			return nil, fmt.Errorf("scheduler.event.publish is unavailable during validation")
		}
		args := call.Args()
		if len(args) < 2 {
			return nil, fmt.Errorf("scheduler.event.publish requires a topic and payload")
		}
		topic := strings.TrimSpace(args[0].String())
		if topic == "" {
			return nil, fmt.Errorf("scheduler.event.publish requires a non-empty topic")
		}
		if args[1].IsUndefined() || args[1].IsNull() || !args[1].IsObject() || args[1].IsArray() {
			return nil, fmt.Errorf("scheduler.event.publish payload must be an object")
		}
		payloadJSON, err := jsValueToJSON(args[1])
		if err != nil {
			return nil, fmt.Errorf("encode scheduler.event.publish payload: %w", err)
		}
		record, err := state.host.PublishEvent(state.ctx, topic, payloadJSON)
		if err != nil {
			return nil, err
		}
		responseJSON, err := marshalJSONCompact(map[string]any{
			"eventId":       record.ID,
			"sequence":      record.Sequence,
			"topic":         record.Topic,
			"correlationId": record.CorrelationID,
		})
		if err != nil {
			return nil, err
		}
		return payloadValueFromJSON(jsctx, responseJSON)
	})
	eventObj.SetPropertyStr("publish", eventPublishFn)
	schedulerObj.SetPropertyStr("event", eventObj)

	agentFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if state.host == nil {
			return nil, fmt.Errorf("scheduler.agent is unavailable during validation")
		}
		args := call.Args()
		if len(args) == 0 {
			return nil, fmt.Errorf("scheduler.agent requires a prompt")
		}
		prompt := strings.TrimSpace(args[0].String())
		if prompt == "" {
			return nil, fmt.Errorf("scheduler.agent requires a non-empty prompt")
		}
		options, err := parseLoaderAgentRequest(args)
		if err != nil {
			return nil, err
		}
		var outputSchemaValue *qjs.Value
		options.OutputSchema, outputSchemaValue, err = parseLoaderOutputSchema(jsctx, args, "scheduler.agent")
		if err != nil {
			return nil, err
		}
		response, err := state.host.Agent(state.ctx, prompt, options)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(options.OutputSchema) != "" {
			jsonValue, err := loaderJSONResult(firstNonEmpty(response.FinalText, response.Text, response.Output), options.OutputSchema, "agent finalText")
			if err != nil {
				return nil, err
			}
			response.JSON = jsonValue
		}
		data, err := json.Marshal(response)
		if err != nil {
			return nil, fmt.Errorf("encode scheduler.agent response: %w", err)
		}
		value, err := payloadValueFromJSON(jsctx, string(data))
		if err != nil {
			return nil, fmt.Errorf("decode scheduler.agent response: %w", err)
		}
		if strings.TrimSpace(options.OutputSchema) != "" {
			if err := validateLoaderJSONWithSchema(jsctx, outputSchemaValue, value, "agent"); err != nil {
				return nil, err
			}
		}
		return value, nil
	})
	schedulerObj.SetPropertyStr("agent", agentFn)

	execFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if state.host == nil {
			return nil, fmt.Errorf("scheduler.exec is unavailable during validation")
		}
		request, err := parseLoaderExecRequest(call.Args())
		if err != nil {
			return nil, err
		}
		response, err := state.host.Command(state.ctx, request)
		if err != nil {
			return nil, err
		}
		return loaderCommandResultValue(jsctx, "scheduler.exec", response)
	})
	schedulerObj.SetPropertyStr("exec", execFn)

	shellFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if state.host == nil {
			return nil, fmt.Errorf("scheduler.shell is unavailable during validation")
		}
		request, err := parseLoaderShellRequest(call.Args())
		if err != nil {
			return nil, err
		}
		response, err := state.host.Command(state.ctx, request)
		if err != nil {
			return nil, err
		}
		return loaderCommandResultValue(jsctx, "scheduler.shell", response)
	})
	schedulerObj.SetPropertyStr("shell", shellFn)

	llmFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if state.host == nil {
			return nil, fmt.Errorf("scheduler.llm is unavailable during validation")
		}
		args := call.Args()
		if len(args) == 0 {
			return nil, fmt.Errorf("scheduler.llm requires a prompt")
		}
		prompt := strings.TrimSpace(args[0].String())
		if prompt == "" {
			return nil, fmt.Errorf("scheduler.llm requires a non-empty prompt")
		}
		options, err := parseLoaderLLMRequest(args)
		if err != nil {
			return nil, err
		}
		var outputSchemaValue *qjs.Value
		options.OutputSchema, outputSchemaValue, err = parseLoaderOutputSchema(jsctx, args, "scheduler.llm")
		if err != nil {
			return nil, err
		}
		response, err := state.host.LLM(state.ctx, prompt, options)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(options.OutputSchema) != "" {
			jsonValue, err := loaderJSONResult(response.Text, options.OutputSchema, "llm text")
			if err != nil {
				return nil, err
			}
			response.JSON = jsonValue
		}
		data, err := json.Marshal(response)
		if err != nil {
			return nil, fmt.Errorf("encode scheduler.llm response: %w", err)
		}
		value, err := payloadValueFromJSON(jsctx, string(data))
		if err != nil {
			return nil, fmt.Errorf("decode scheduler.llm response: %w", err)
		}
		if strings.TrimSpace(options.OutputSchema) != "" {
			if err := validateLoaderJSONWithSchema(jsctx, outputSchemaValue, value, "llm"); err != nil {
				return nil, err
			}
		}
		return value, nil
	})
	schedulerObj.SetPropertyStr("llm", llmFn)

	stateObj := jsctx.NewObject()
	stateGetFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if state.host == nil {
			return jsctx.NewUndefined(), nil
		}
		args := call.Args()
		if len(args) == 0 {
			return nil, fmt.Errorf("scheduler.state.get requires a key")
		}
		key := strings.TrimSpace(args[0].String())
		if key == "" {
			return nil, fmt.Errorf("scheduler.state.get requires a non-empty key")
		}
		valueJSON, ok, err := state.host.StateGet(state.ctx, key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return jsctx.NewUndefined(), nil
		}
		value, err := payloadValueFromJSON(jsctx, valueJSON)
		if err != nil {
			return nil, err
		}
		return value, nil
	})
	stateSetFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if state.host == nil {
			return jsctx.NewUndefined(), nil
		}
		args := call.Args()
		if len(args) < 2 {
			return nil, fmt.Errorf("scheduler.state.set requires a key and value")
		}
		key := strings.TrimSpace(args[0].String())
		if key == "" {
			return nil, fmt.Errorf("scheduler.state.set requires a non-empty key")
		}
		if args[1].IsUndefined() {
			if err := state.host.StateDelete(state.ctx, key); err != nil {
				return nil, err
			}
			return jsctx.NewUndefined(), nil
		}
		valueJSON, err := jsValueToJSON(args[1])
		if err != nil {
			return nil, err
		}
		if err := state.host.StateSet(state.ctx, key, valueJSON); err != nil {
			return nil, err
		}
		return jsctx.NewUndefined(), nil
	})
	stateDeleteFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
		if state.host == nil {
			return jsctx.NewUndefined(), nil
		}
		args := call.Args()
		if len(args) == 0 {
			return nil, fmt.Errorf("scheduler.state.delete requires a key")
		}
		key := strings.TrimSpace(args[0].String())
		if key == "" {
			return nil, fmt.Errorf("scheduler.state.delete requires a non-empty key")
		}
		if err := state.host.StateDelete(state.ctx, key); err != nil {
			return nil, err
		}
		return jsctx.NewUndefined(), nil
	})
	stateObj.SetPropertyStr("get", stateGetFn)
	stateObj.SetPropertyStr("set", stateSetFn)
	stateObj.SetPropertyStr("delete", stateDeleteFn)
	schedulerObj.SetPropertyStr("state", stateObj)

	sessionObj := jsctx.NewObject()
	sessionMethods := agentcomposev1.File_proto_agentcompose_v1_agentcompose_proto.Services().ByName("SessionService").Methods()
	for index := 0; index < sessionMethods.Len(); index++ {
		methodName := string(sessionMethods.Get(index).Name())
		jsName := lowerFirstASCII(methodName)
		apiName := "scheduler.session." + jsName
		methodNameCopy := methodName
		apiNameCopy := apiName
		sessionFn := jsctx.Function(func(call *qjs.This) (*qjs.Value, error) {
			if state.host == nil {
				return nil, fmt.Errorf("%s is unavailable during validation", apiNameCopy)
			}
			requestJSON, err := loaderRPCRequestJSON(call.Args(), apiNameCopy)
			if err != nil {
				return nil, err
			}
			responseJSON, err := state.host.CallSessionRPC(state.ctx, methodNameCopy, requestJSON)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", apiNameCopy, err)
			}
			response, err := payloadValueFromJSON(jsctx, responseJSON)
			if err != nil {
				return nil, fmt.Errorf("decode %s response: %w", apiNameCopy, err)
			}
			return response, nil
		})
		sessionObj.SetPropertyStr(jsName, sessionFn)
		if jsName != methodName {
			sessionObj.SetPropertyStr(methodName, sessionFn.Clone())
		}
	}
	schedulerObj.SetPropertyStr("session", sessionObj)

	runtimeObj := jsctx.NewObject()
	runtimeObj.SetPropertyStr("name", jsctx.NewString("scheduler"))
	schedulerObj.SetPropertyStr("runtime", runtimeObj)
	return schedulerObj, nil
}

const loaderSchemaBuilderScript = `
(function () {
  function schema(json, validate) {
    return {
      toJSONSchema: function () { return json; },
      parse: function (value) {
        validate(value);
        return value;
      }
    };
  }
  function schemaToJSON(value) {
    if (value && typeof value.toJSONSchema === "function") {
      return value.toJSONSchema();
    }
    return value;
  }
  scheduler.z = {
    string: function () {
      return schema({ type: "string" }, function (value) {
        if (typeof value !== "string") throw new Error("expected string");
      });
    },
    number: function () {
      return schema({ type: "number" }, function (value) {
        if (typeof value !== "number") throw new Error("expected number");
      });
    },
    boolean: function () {
      return schema({ type: "boolean" }, function (value) {
        if (typeof value !== "boolean") throw new Error("expected boolean");
      });
    },
    enum: function (values) {
      return schema({ type: "string", enum: values.slice() }, function (value) {
        if (values.indexOf(value) === -1) throw new Error("expected one of " + values.join(", "));
      });
    },
    array: function (item) {
      return schema({ type: "array", items: schemaToJSON(item) }, function (value) {
        if (!Array.isArray(value)) throw new Error("expected array");
        if (item && typeof item.parse === "function") {
          for (const entry of value) item.parse(entry);
        }
      });
    },
    object: function (shape) {
      const properties = {};
      const required = [];
      for (const key of Object.keys(shape || {})) {
        properties[key] = schemaToJSON(shape[key]);
        required.push(key);
      }
      return schema({ type: "object", properties, required, additionalProperties: false }, function (value) {
        if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("expected object");
        for (const key of required) {
          if (!(key in value)) throw new Error("missing required property " + key);
          if (shape[key] && typeof shape[key].parse === "function") shape[key].parse(value[key]);
        }
        for (const key of Object.keys(value)) {
          if (!(key in properties)) throw new Error("unexpected property " + key);
        }
      });
    }
  };
})();
`

func installLoaderSchemaBuilder(jsctx *qjs.Context) error {
	value, err := jsctx.Eval("scheduler-z.js", qjs.Code(loaderSchemaBuilderScript))
	if err != nil {
		return fmt.Errorf("install scheduler.z schema builder: %w", err)
	}
	if value != nil {
		value.Free()
	}
	return nil
}

func (e *QJSLoaderEngine) executeRequestedHandler(jsctx *qjs.Context, state *loaderExecutionState, trigger *LoaderTrigger, payload *qjs.Value) (*qjs.Value, error) {
	global := jsctx.Global()
	if trigger != nil && strings.TrimSpace(trigger.ID) != "" {
		for _, registration := range state.registrations {
			if registration.trigger.ID != strings.TrimSpace(trigger.ID) {
				continue
			}
			return jsctx.Invoke(registration.callback, global, payload)
		}
		return nil, fmt.Errorf("loader trigger %s not found in script", strings.TrimSpace(trigger.ID))
	}

	mainFn := global.GetPropertyStr("main")
	if mainFn.IsFunction() {
		return jsctx.Invoke(mainFn, global, payload)
	}

	if len(state.registrations) == 1 {
		return jsctx.Invoke(state.registrations[0].callback, global, payload)
	}
	if len(state.registrations) > 1 {
		return nil, fmt.Errorf("loader defines multiple triggers; choose a trigger explicitly or define main()")
	}
	return jsctx.NewUndefined(), nil
}

func (s *loaderExecutionState) register(trigger LoaderTrigger, callback *qjs.Value) error {
	if _, ok := s.seenIDs[trigger.ID]; ok {
		return fmt.Errorf("duplicate loader trigger id %q", trigger.ID)
	}
	cloned := callback.Clone()
	s.seenIDs[trigger.ID] = struct{}{}
	s.registrations = append(s.registrations, loaderRegistration{
		trigger:  trigger,
		callback: cloned,
		order:    len(s.registrations),
	})
	return nil
}

func (s *loaderExecutionState) unregister(id string, kinds ...string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	allowedKinds := make(map[string]struct{}, len(kinds))
	for _, kind := range kinds {
		allowedKinds[strings.TrimSpace(kind)] = struct{}{}
	}
	next := make([]loaderRegistration, 0, len(s.registrations))
	removed := false
	for _, item := range s.registrations {
		if item.trigger.ID == id {
			if len(allowedKinds) == 0 {
				removed = true
				item.callback = nil
				continue
			}
			if _, ok := allowedKinds[item.trigger.Kind]; ok {
				removed = true
				item.callback = nil
				continue
			}
		}
		item.order = len(next)
		next = append(next, item)
	}
	if removed {
		delete(s.seenIDs, id)
		s.registrations = next
	}
	return removed
}

func (s *loaderExecutionState) freeCallbacks() {
	for i := range s.registrations {
		s.registrations[i].callback = nil
	}
}

func (s *loaderExecutionState) triggers() []LoaderTrigger {
	items := make([]LoaderTrigger, 0, len(s.registrations))
	for _, item := range s.registrations {
		items = append(items, item.trigger)
	}
	return items
}

func parseIntervalRegistration(args []*qjs.Value) (string, int64, *qjs.Value, bool, error) {
	return parseTimerRegistration("scheduler.interval", "interval", args)
}

func parseTimeoutRegistration(args []*qjs.Value) (string, int64, *qjs.Value, bool, error) {
	return parseTimerRegistration("scheduler.timeout", "delay", args)
}

func parseTimerRegistration(name, delayLabel string, args []*qjs.Value) (string, int64, *qjs.Value, bool, error) {
	if len(args) < 2 {
		return "", 0, nil, false, fmt.Errorf("%s requires a callback and %s", name, delayLabel)
	}
	var id string
	var callback *qjs.Value
	var delayMs int64
	autoID := false
	switch {
	case args[0].IsFunction():
		callback = args[0]
		delayMs = args[1].Int64()
		if len(args) > 2 && args[2].IsString() {
			id = strings.TrimSpace(args[2].String())
		}
	case args[0].IsNumber() && args[1].IsFunction():
		delayMs = args[0].Int64()
		callback = args[1]
		if len(args) > 2 && args[2].IsString() {
			id = strings.TrimSpace(args[2].String())
		}
	case args[0].IsString() && len(args) > 2 && args[1].IsFunction():
		id = strings.TrimSpace(args[0].String())
		callback = args[1]
		delayMs = args[2].Int64()
	case args[0].IsString() && len(args) > 2 && args[2].IsFunction():
		id = strings.TrimSpace(args[0].String())
		delayMs = args[1].Int64()
		callback = args[2]
	default:
		return "", 0, nil, false, fmt.Errorf("unsupported %s signature", name)
	}
	if callback == nil || !callback.IsFunction() {
		return "", 0, nil, false, fmt.Errorf("%s requires a callback function", name)
	}
	if delayMs <= 0 {
		return "", 0, nil, false, fmt.Errorf("%s requires a positive %s", name, delayLabel)
	}
	if id == "" {
		autoID = true
	}
	return id, delayMs, callback, autoID, nil
}

func triggerHandle(value *qjs.Value) string {
	if value == nil || value.IsNull() || value.IsUndefined() {
		return ""
	}
	return strings.TrimSpace(value.String())
}

func parseCronRegistration(args []*qjs.Value) (string, string, *qjs.Value, string, bool, error) {
	if len(args) < 2 {
		return "", "", nil, "", false, fmt.Errorf("scheduler.cron requires an expression and callback")
	}
	var (
		expr     string
		id       string
		callback *qjs.Value
		timezone string
		err      error
	)
	switch {
	case args[0].IsString() && args[1].IsFunction():
		expr = strings.TrimSpace(args[0].String())
		callback = args[1]
		id, timezone, err = parseCronRegistrationOptions(args[2:]...)
	case len(args) > 2 && args[0].IsString() && args[1].IsString() && args[2].IsFunction():
		id = strings.TrimSpace(args[0].String())
		expr = strings.TrimSpace(args[1].String())
		callback = args[2]
		optionID, optionTimezone, optionErr := parseCronRegistrationOptions(args[3:]...)
		if optionErr != nil {
			err = optionErr
			break
		}
		if optionID != "" && optionID != id {
			err = fmt.Errorf("scheduler.cron received multiple trigger ids")
			break
		}
		timezone = optionTimezone
	default:
		return "", "", nil, "", false, fmt.Errorf("unsupported scheduler.cron signature")
	}
	if err != nil {
		return "", "", nil, "", false, err
	}
	if callback == nil || !callback.IsFunction() {
		return "", "", nil, "", false, fmt.Errorf("scheduler.cron requires a callback function")
	}
	if strings.TrimSpace(expr) == "" {
		return "", "", nil, "", false, fmt.Errorf("scheduler.cron requires a non-empty expression")
	}
	specJSON, err := loaderCronSpecJSON(expr, timezone)
	if err != nil {
		return "", "", nil, "", false, err
	}
	autoID := strings.TrimSpace(id) == ""
	return expr, id, callback, specJSON, autoID, nil
}

func parseCronRegistrationOptions(args ...*qjs.Value) (string, string, error) {
	if len(args) == 0 {
		return "", "", nil
	}
	if len(args) > 1 {
		return "", "", fmt.Errorf("scheduler.cron accepts at most one options argument")
	}
	value := args[0]
	if value == nil || value.IsUndefined() || value.IsNull() {
		return "", "", nil
	}
	if value.IsString() {
		return strings.TrimSpace(value.String()), "", nil
	}
	options, err := qjs.ToGoValue[map[string]any](value)
	if err != nil {
		return "", "", fmt.Errorf("decode scheduler.cron options: %w", err)
	}
	var id string
	var timezone string
	if raw, ok := options["id"].(string); ok {
		id = strings.TrimSpace(raw)
	}
	if raw, ok := options["timezone"].(string); ok {
		timezone = strings.TrimSpace(raw)
	} else if raw, ok := options["tz"].(string); ok {
		timezone = strings.TrimSpace(raw)
	}
	return id, timezone, nil
}

func parseEventRegistration(args []*qjs.Value) (string, string, *qjs.Value, bool, error) {
	if len(args) < 2 {
		return "", "", nil, false, fmt.Errorf("scheduler.on requires a topic and callback")
	}
	topic := strings.TrimSpace(args[0].String())
	if topic == "" {
		return "", "", nil, false, fmt.Errorf("scheduler.on requires a non-empty topic")
	}
	var id string
	var callback *qjs.Value
	autoID := false
	if args[1].IsFunction() {
		callback = args[1]
		if len(args) > 2 && args[2].IsString() {
			id = strings.TrimSpace(args[2].String())
		}
	} else if len(args) > 2 && args[1].IsString() && args[2].IsFunction() {
		id = strings.TrimSpace(args[1].String())
		callback = args[2]
	} else {
		return "", "", nil, false, fmt.Errorf("unsupported scheduler.on signature")
	}
	if id == "" {
		autoID = true
	}
	return topic, id, callback, autoID, nil
}

func parseLoaderAgentRequest(args []*qjs.Value) (LoaderAgentRequest, error) {
	request := LoaderAgentRequest{}
	if len(args) < 2 || args[1] == nil || args[1].IsUndefined() || args[1].IsNull() {
		return request, nil
	}
	options, err := loaderAgentOptionsWithoutSchema(args[1])
	if err != nil {
		return LoaderAgentRequest{}, fmt.Errorf("decode scheduler.agent options: %w", err)
	}
	request.Agent = normalizeAgentKind(loaderStringOption(options, "agent"))
	request.SessionPolicy = normalizeLoaderSessionPolicy(loaderStringOption(options, "sessionPolicy", "session_policy"))
	request.Timeout, err = loaderDurationOption(options, "timeout", "agentTimeout", "agent_timeout")
	if err != nil {
		return LoaderAgentRequest{}, fmt.Errorf("decode scheduler.agent timeout: %w", err)
	}
	request.Title = loaderStringOption(options, "title")
	request.Driver = loaderStringOption(options, "driver")
	request.GuestImage = loaderStringOption(options, "guestImage", "guest_image")
	request.PullPolicy = normalizeImagePullPolicy(loaderStringOption(options, "pullPolicy", "pull_policy"))
	request.WorkspaceID = loaderStringOption(options, "workspaceId", "workspace_id")
	request.SessionEnv, err = loaderSessionEnvOption(options)
	if err != nil {
		return LoaderAgentRequest{}, err
	}
	return request, nil
}

func loaderAgentOptionsWithoutSchema(value *qjs.Value) (map[string]any, error) {
	if value == nil || value.IsUndefined() || value.IsNull() {
		return map[string]any{}, nil
	}
	if !value.IsObject() || value.IsArray() {
		return qjs.ToGoValue[map[string]any](value)
	}
	rawJSON, err := value.JSONStringify()
	if err != nil {
		return nil, err
	}
	var options map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &options); err != nil {
		return nil, err
	}
	delete(options, "outputSchema")
	delete(options, "schema")
	return options, nil
}

func parseLoaderOutputSchema(jsctx *qjs.Context, args []*qjs.Value, apiName string) (string, *qjs.Value, error) {
	if len(args) < 2 || args[1] == nil || args[1].IsUndefined() || args[1].IsNull() {
		return "", nil, nil
	}
	if !args[1].IsObject() || args[1].IsArray() {
		return "", nil, nil
	}
	options := args[1]
	for _, key := range []string{"outputSchema", "schema"} {
		schemaValue := options.GetPropertyStr(key)
		if schemaValue == nil || schemaValue.IsUndefined() || schemaValue.IsNull() {
			continue
		}
		schemaJSON, err := loaderOutputSchemaJSON(jsctx, schemaValue)
		if err != nil {
			return "", nil, fmt.Errorf("decode %s %s: %w", apiName, key, err)
		}
		return schemaJSON, schemaValue, nil
	}
	return "", nil, nil
}

func loaderOutputSchemaJSON(jsctx *qjs.Context, value *qjs.Value) (string, error) {
	if !value.IsObject() || value.IsArray() {
		return "", fmt.Errorf("must be an object")
	}
	toJSONSchema := value.GetPropertyStr("toJSONSchema")
	if toJSONSchema != nil && toJSONSchema.IsFunction() {
		converted, err := jsctx.Invoke(toJSONSchema, value)
		if err != nil {
			return "", err
		}
		if converted == nil || converted.IsUndefined() || converted.IsNull() || !converted.IsObject() || converted.IsArray() {
			return "", fmt.Errorf("toJSONSchema must return an object")
		}
		return jsValueToJSON(converted)
	}
	return jsValueToJSON(value)
}

func validateLoaderJSONWithSchema(jsctx *qjs.Context, schemaValue, responseValue *qjs.Value, apiName string) error {
	if schemaValue == nil || responseValue == nil || !schemaValue.IsObject() {
		return nil
	}
	parseFn := schemaValue.GetPropertyStr("parse")
	if parseFn == nil || !parseFn.IsFunction() {
		return nil
	}
	jsonValue := responseValue.GetPropertyStr("json")
	if jsonValue == nil || jsonValue.IsUndefined() || jsonValue.IsNull() {
		return nil
	}
	if _, err := jsctx.Invoke(parseFn, schemaValue, jsonValue); err != nil {
		return fmt.Errorf("%s JSON output does not match outputSchema: %w", apiName, err)
	}
	return nil
}

func parseLoaderExecRequest(args []*qjs.Value) (LoaderCommandRequest, error) {
	if len(args) != 1 || args[0] == nil || args[0].IsUndefined() || args[0].IsNull() || !args[0].IsObject() || args[0].IsArray() {
		return LoaderCommandRequest{}, fmt.Errorf("scheduler.exec requires a request object")
	}
	options, err := qjs.ToGoValue[map[string]any](args[0])
	if err != nil {
		return LoaderCommandRequest{}, fmt.Errorf("decode scheduler.exec request: %w", err)
	}
	request, err := loaderCommandRequestFromOptions(options, "scheduler.exec")
	if err != nil {
		return LoaderCommandRequest{}, err
	}
	request.Mode = "exec"
	request.Command = loaderStringOption(options, "command")
	if strings.TrimSpace(request.Command) == "" {
		return LoaderCommandRequest{}, fmt.Errorf("scheduler.exec requires a non-empty command")
	}
	request.Args, err = loaderStringArrayOption(options, "args")
	if err != nil {
		return LoaderCommandRequest{}, fmt.Errorf("decode scheduler.exec args: %w", err)
	}
	return request, nil
}

func parseLoaderShellRequest(args []*qjs.Value) (LoaderCommandRequest, error) {
	if len(args) == 0 || args[0] == nil || args[0].IsUndefined() || args[0].IsNull() {
		return LoaderCommandRequest{}, fmt.Errorf("scheduler.shell requires a script")
	}
	if len(args) > 2 {
		return LoaderCommandRequest{}, fmt.Errorf("scheduler.shell accepts a script and optional options object")
	}
	script := args[0].String()
	if strings.TrimSpace(script) == "" {
		return LoaderCommandRequest{}, fmt.Errorf("scheduler.shell requires a non-empty script")
	}
	options := map[string]any{}
	if len(args) > 1 && args[1] != nil && !args[1].IsUndefined() && !args[1].IsNull() {
		if !args[1].IsObject() || args[1].IsArray() {
			return LoaderCommandRequest{}, fmt.Errorf("scheduler.shell options must be an object")
		}
		decoded, err := qjs.ToGoValue[map[string]any](args[1])
		if err != nil {
			return LoaderCommandRequest{}, fmt.Errorf("decode scheduler.shell options: %w", err)
		}
		options = decoded
	}
	request, err := loaderCommandRequestFromOptions(options, "scheduler.shell")
	if err != nil {
		return LoaderCommandRequest{}, err
	}
	request.Mode = "shell"
	request.Script = script
	return request, nil
}

func loaderCommandRequestFromOptions(options map[string]any, apiName string) (LoaderCommandRequest, error) {
	var err error
	request := LoaderCommandRequest{
		Cwd:           loaderStringOption(options, "cwd"),
		SessionPolicy: normalizeLoaderSessionPolicy(loaderStringOption(options, "sessionPolicy", "session_policy")),
		Title:         loaderStringOption(options, "title"),
		Driver:        loaderStringOption(options, "driver"),
		GuestImage:    loaderStringOption(options, "guestImage", "guest_image"),
		PullPolicy:    normalizeImagePullPolicy(loaderStringOption(options, "pullPolicy", "pull_policy")),
		WorkspaceID:   loaderStringOption(options, "workspaceId", "workspace_id"),
	}
	request.Env, err = loaderStringMapOption(options, "env")
	if err != nil {
		return LoaderCommandRequest{}, fmt.Errorf("decode %s env: %w", apiName, err)
	}
	request.TimeoutMs, err = loaderInt64Option(options, "timeoutMs", "timeout_ms")
	if err != nil {
		return LoaderCommandRequest{}, fmt.Errorf("decode %s timeoutMs: %w", apiName, err)
	}
	request.MaxOutputBytes, err = loaderInt64Option(options, "maxOutputBytes", "max_output_bytes")
	if err != nil {
		return LoaderCommandRequest{}, fmt.Errorf("decode %s maxOutputBytes: %w", apiName, err)
	}
	request.SessionEnv, err = loaderSessionEnvOption(options)
	if err != nil {
		return LoaderCommandRequest{}, fmt.Errorf("%s", strings.Replace(err.Error(), "scheduler.agent", apiName, 1))
	}
	return request, nil
}

func loaderCommandResultValue(jsctx *qjs.Context, apiName string, response LoaderCommandResult) (*qjs.Value, error) {
	data, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("encode %s response: %w", apiName, err)
	}
	value, err := payloadValueFromJSON(jsctx, string(data))
	if err != nil {
		return nil, fmt.Errorf("decode %s response: %w", apiName, err)
	}
	return value, nil
}

func loaderDurationOption(options map[string]any, keys ...string) (time.Duration, error) {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		switch raw := value.(type) {
		case string:
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				return 0, nil
			}
			parsed, err := time.ParseDuration(trimmed)
			if err != nil {
				return 0, err
			}
			if parsed <= 0 {
				return 0, fmt.Errorf("duration must be positive")
			}
			return parsed, nil
		default:
			return 0, fmt.Errorf("duration must be a string")
		}
	}
	return 0, nil
}

func loaderStringOption(options map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		if raw, ok := value.(string); ok {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

func loaderStringArrayOption(options map[string]any, keys ...string) ([]string, error) {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		rawItems, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("must be an array")
		}
		items := make([]string, 0, len(rawItems))
		for index, rawItem := range rawItems {
			if rawItem == nil {
				return nil, fmt.Errorf("item %d must be a string", index)
			}
			item, ok := rawItem.(string)
			if !ok {
				return nil, fmt.Errorf("item %d must be a string", index)
			}
			items = append(items, item)
		}
		return items, nil
	}
	return nil, nil
}

func loaderStringMapOption(options map[string]any, keys ...string) (map[string]string, error) {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		rawItems, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("must be an object")
		}
		items := make(map[string]string, len(rawItems))
		for rawName, rawValue := range rawItems {
			name := strings.TrimSpace(rawName)
			if name == "" {
				continue
			}
			if rawValue == nil {
				items[name] = ""
				continue
			}
			value, ok := rawValue.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a string", name)
			}
			items[name] = value
		}
		return items, nil
	}
	return nil, nil
}

func loaderInt64Option(options map[string]any, keys ...string) (int64, error) {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return int64(typed), nil
		case int64:
			return typed, nil
		case int:
			return int64(typed), nil
		default:
			return 0, fmt.Errorf("must be a number")
		}
	}
	return 0, nil
}

func loaderSessionEnvOption(options map[string]any) ([]SessionEnvVar, error) {
	for _, key := range []string{"sessionEnv", "session_env"} {
		value, ok := options[key]
		if !ok {
			continue
		}
		items, err := loaderSessionEnvItems(value)
		if err != nil {
			return nil, fmt.Errorf("decode scheduler.agent %s: %w", key, err)
		}
		return items, nil
	}
	return nil, nil
}

func loaderSessionEnvItems(value any) ([]SessionEnvVar, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if strings.TrimSpace(key) == "" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := make([]SessionEnvVar, 0, len(keys))
		for _, key := range keys {
			name := strings.TrimSpace(key)
			if name == "" {
				continue
			}
			envValue, secret, err := loaderSessionEnvValue(name, typed[key])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			items = append(items, SessionEnvVar{Name: name, Value: envValue, Secret: secret})
		}
		return normalizeEnvItems(items), nil
	case []any:
		items := make([]SessionEnvVar, 0, len(typed))
		for index, rawItem := range typed {
			entry, ok := rawItem.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("item %d must be an object", index)
			}
			name := loaderStringOption(entry, "name")
			if name == "" {
				return nil, fmt.Errorf("item %d requires a non-empty name", index)
			}
			envValue, secret, err := loaderSessionEnvValue(name, entry["value"])
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			if rawSecret, ok := entry["secret"]; ok && rawSecret != nil {
				switch typedSecret := rawSecret.(type) {
				case bool:
					secret = typedSecret
				case string:
					secret = strings.EqualFold(strings.TrimSpace(typedSecret), "true")
				case float64:
					secret = typedSecret != 0
				default:
					return nil, fmt.Errorf("item %d secret must be a boolean", index)
				}
			}
			items = append(items, SessionEnvVar{Name: name, Value: envValue, Secret: secret})
		}
		return normalizeEnvItems(items), nil
	default:
		return nil, fmt.Errorf("must be an object map or array")
	}
}

func loaderSessionEnvValue(name string, value any) (string, bool, error) {
	secret := loaderSecretEnvName(name)
	switch typed := value.(type) {
	case nil:
		return "", secret, nil
	case string:
		return typed, secret, nil
	case bool:
		if typed {
			return "true", secret, nil
		}
		return "false", secret, nil
	case float64:
		return strings.TrimSpace(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", typed), "0"), ".")), secret, nil
	case map[string]any:
		envValue, nestedSecret, err := loaderSessionEnvValue(name, typed["value"])
		if err != nil {
			return "", false, err
		}
		secret = nestedSecret
		if rawSecret, ok := typed["secret"]; ok && rawSecret != nil {
			switch typedSecret := rawSecret.(type) {
			case bool:
				secret = typedSecret
			case string:
				secret = strings.EqualFold(strings.TrimSpace(typedSecret), "true")
			case float64:
				secret = typedSecret != 0
			default:
				return "", false, fmt.Errorf("secret must be a boolean")
			}
		}
		return envValue, secret, nil
	default:
		return fmt.Sprint(typed), secret, nil
	}
}

func loaderSecretEnvName(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	if strings.Contains(name, "PASSWORD") || strings.HasSuffix(name, "_TOKEN") || strings.HasSuffix(name, "_SECRET") || strings.HasSuffix(name, "_KEY") {
		return true
	}
	switch name {
	case "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY", "LLM_API_KEY":
		return true
	default:
		return false
	}
}

func parseLoaderLLMRequest(args []*qjs.Value) (LoaderLLMRequest, error) {
	request := LoaderLLMRequest{}
	if len(args) < 2 || args[1] == nil || args[1].IsUndefined() || args[1].IsNull() {
		return request, nil
	}
	options, err := loaderAgentOptionsWithoutSchema(args[1])
	if err != nil {
		return LoaderLLMRequest{}, fmt.Errorf("decode scheduler.llm options: %w", err)
	}
	if value, ok := options["model"].(string); ok {
		request.Model = strings.TrimSpace(value)
	}
	return request, nil
}

func loaderRPCRequestJSON(args []*qjs.Value, apiName string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if len(args) > 1 {
		return "", fmt.Errorf("%s accepts at most one request object", apiName)
	}
	value := args[0]
	if value == nil || value.IsNull() || value.IsUndefined() {
		return "", nil
	}
	requestJSON, err := jsValueToJSON(value)
	if err != nil {
		return "", fmt.Errorf("encode %s request: %w", apiName, err)
	}
	return strings.TrimSpace(requestJSON), nil
}

func lowerFirstASCII(value string) string {
	if value == "" {
		return ""
	}
	if len(value) == 1 {
		return strings.ToLower(value)
	}
	return strings.ToLower(value[:1]) + value[1:]
}

func payloadValueFromJSON(jsctx *qjs.Context, payloadJSON string) (*qjs.Value, error) {
	payloadJSON = strings.TrimSpace(payloadJSON)
	if payloadJSON == "" {
		return jsctx.NewUndefined(), nil
	}
	return jsctx.ParseJSON(payloadJSON), nil
}

func jsValueToJSON(value *qjs.Value) (string, error) {
	if value == nil || value.IsUndefined() {
		return "", nil
	}
	if value.IsNull() {
		return "null", nil
	}
	if value.IsBool() {
		if value.Bool() {
			return "true", nil
		}
		return "false", nil
	}
	if value.IsString() || value.IsBigInt() {
		data, err := json.Marshal(value.String())
		if err != nil {
			return "", fmt.Errorf("encode js string value: %w", err)
		}
		return string(data), nil
	}
	if value.IsNumber() {
		raw := strings.TrimSpace(value.String())
		if raw == "" {
			return "", nil
		}
		if raw == "NaN" || raw == "Infinity" || raw == "-Infinity" {
			data, err := json.Marshal(raw)
			if err != nil {
				return "", fmt.Errorf("encode js numeric sentinel: %w", err)
			}
			return string(data), nil
		}
		if json.Valid([]byte(raw)) {
			return raw, nil
		}
	}
	jsonValue, err := value.JSONStringify()
	if err == nil && strings.TrimSpace(jsonValue) != "" && json.Valid([]byte(jsonValue)) {
		return jsonValue, nil
	}
	return "", nil
}

func loaderResultJSON(value *qjs.Value) (string, bool, error) {
	if value == nil || value.IsUndefined() {
		return "", false, nil
	}
	jsonValue, err := jsValueToJSON(value)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(jsonValue) == "" {
		return "", false, nil
	}
	return jsonValue, true, nil
}

func normalizeImagePullPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "always":
		return "always"
	case "missing":
		return "missing"
	case "never":
		return "never"
	default:
		return ""
	}
}
