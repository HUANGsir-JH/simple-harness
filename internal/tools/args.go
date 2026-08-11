package tools

import (
	"encoding/json"
	"reflect"
	"sync"

	"github.com/invopop/jsonschema"
)

// jsonschemaReflector 从参数类型生成 JSON Schema 的反射器配置（C4）：
//   - DoNotReference：顶层直接输出 properties/required，不包 $ref/$defs——
//     anthropic 兼容端点的 toAnthropicInputSchema 从顶层读 properties/required
//   - Anonymous：不生成从包路径派生的 $id（纯噪音）
//   - AllowAdditionalProperties：与既有手写 schema 一致（不生成
//     additionalProperties:false，保持模型行为不变；如需严格 schema 改 true 即可）
//
// 库 v0.14.0 已作为 SDK 间接依赖存在，此处提升为直接依赖。
var jsonschemaReflector = &jsonschema.Reflector{
	DoNotReference:            true,
	Anonymous:                 true,
	AllowAdditionalProperties: true,
}

// schemaCache 按参数类型缓存生成的 schema。Spec() 每轮采样都会调用，不能每次
// reflect；Reflector.Reflect 每次用全新本地 definitions，并发安全。
var schemaCache sync.Map // reflect.Type → json.RawMessage

// schemaOf 从参数类型生成并缓存 JSON Schema。类型须为 struct（值或指针）。
// 生成的 Parameters 经 json.Marshal 必然有效——schema 与 Handle 参数解析共用
// 同一类型，杜绝手写双份声明的漂移（C4）。jsonschema.Schema 是纯 struct
// （无 channel/func/环），marshal 错误不可达；panic 暴露编程错误。
func schemaOf[T any]() json.RawMessage {
	t := reflect.TypeOf((*T)(nil)).Elem()
	if v, ok := schemaCache.Load(t); ok {
		return v.(json.RawMessage)
	}
	schema := jsonschemaReflector.Reflect(reflect.New(t).Interface())
	b, err := json.Marshal(schema)
	if err != nil {
		panic("tools: 生成 " + t.String() + " 的 schema: " + err.Error())
	}
	raw := json.RawMessage(b)
	schemaCache.Store(t, raw)
	return raw
}

// parseArgs 解析工具参数；失败包装为 RespondToModel 错误（错误文本回填历史、
// 循环继续，ADR-003）。泛型消除 7 个工具重复的"解析 + 包装"样板（B4）。
func parseArgs[T any](tool string, args json.RawMessage) (T, error) {
	var p T
	if err := json.Unmarshal(args, &p); err != nil {
		return p, &ToolError{RespondToModel: true, Message: tool + ": 参数解析失败: " + err.Error()}
	}
	return p, nil
}
