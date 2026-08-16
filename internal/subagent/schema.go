package subagent

import (
	"encoding/json"
	"reflect"
	"sync"

	"github.com/agent-project/harness/internal/tools"
	"github.com/invopop/jsonschema"
)

// 与 tools 包同款的参数 schema 反射（C4：schema 单一来源，从参数类型生成；
// 复制实现避免 tools 包导出私有 helper——控制工具在本包实现）。

var jsonschemaReflector = &jsonschema.Reflector{
	DoNotReference:            true,
	Anonymous:                 true,
	AllowAdditionalProperties: true,
}

var schemaCache sync.Map // reflect.Type → json.RawMessage

// schemaOf 从参数类型生成并缓存 JSON Schema（同 tools.schemaOf）。
func schemaOf[T any]() json.RawMessage {
	t := reflect.TypeFor[T]()
	if v, ok := schemaCache.Load(t); ok {
		return v.(json.RawMessage)
	}
	schema := jsonschemaReflector.Reflect(reflect.New(t).Interface())
	b, err := json.Marshal(schema)
	if err != nil {
		panic("subagent: 生成 " + t.String() + " 的 schema: " + err.Error())
	}
	raw := json.RawMessage(b)
	schemaCache.Store(t, raw)
	return raw
}

// parseArgs 解析工具参数；失败包装为 RespondToModel 错误（同 tools.parseArgs）。
func parseArgs[T any](tool string, args json.RawMessage) (T, error) {
	var p T
	if err := json.Unmarshal(args, &p); err != nil {
		return p, &tools.ToolError{RespondToModel: true, Message: tool + ": 参数解析失败: " + err.Error()}
	}
	return p, nil
}
