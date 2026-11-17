package toolcheck

import (
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"weave-os/router/internal/observability"
)

var localePrinter = message.NewPrinter(language.English)

type Bucket string

const (
	BucketInvalidJSON Bucket = "invalid_json"

	BucketUnknownTool Bucket = "unknown_tool"

	BucketSchemaMismatch Bucket = "schema_mismatch"
)

const maxDetailBytes = 300

const maxSchemaBytes = 256 * 1024

const maxArgsBytes = 4 * 1024 * 1024

type Issue struct {
	ToolName string
	Bucket   Bucket

	Detail string

	Repaired bool

	Actions []string
}

type Verdict struct {
	OK bool

	Args string

	Issue *Issue
}

type toolSchema struct {
	compiled *jsonschema.Schema

	required map[string]struct{}
}

type Validator struct {
	tools map[string]*toolSchema
}

func Compile(toolsRaw []byte) *Validator {
	parsed := gjson.ParseBytes(toolsRaw)
	if !parsed.IsArray() {
		return nil
	}
	tools := make(map[string]*toolSchema)
	parsed.ForEach(func(_, tool gjson.Result) bool {
		name := tool.Get("name").String()
		if name == "" {
			return true
		}
		ts := &toolSchema{required: make(map[string]struct{})}
		schema := tool.Get("input_schema")
		schema.Get("required").ForEach(func(_, r gjson.Result) bool {
			ts.required[r.String()] = struct{}{}
			return true
		})
		ts.compiled = compileSchema(name, schema)
		tools[name] = ts
		return true
	})
	if len(tools) == 0 {
		return nil
	}
	return &Validator{tools: tools}
}

func compileSchema(name string, schema gjson.Result) (compiled *jsonschema.Schema) {
	if !schema.IsObject() || len(schema.Raw) > maxSchemaBytes {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			observability.Get().Warn("toolcheck: schema compile panic", "tool_name", name, "panic", fmt.Sprint(r))
			compiled = nil
		}
	}()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schema.Raw))
	if err != nil {
		observability.Get().Warn("toolcheck: schema unmarshal failed", "tool_name", name, "err", err)
		return nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft7)
	const url = "inmem://tool/input_schema.json"
	if err := compiler.AddResource(url, doc); err != nil {
		observability.Get().Warn("toolcheck: schema add resource failed", "tool_name", name, "err", err)
		return nil
	}
	compiled, err = compiler.Compile(url)
	if err != nil {
		observability.Get().Warn("toolcheck: schema compile failed", "tool_name", name, "err", err)
		return nil
	}
	return compiled
}

func (v *Validator) KnownTool(name string) bool {
	if v == nil || len(v.tools) == 0 {
		return true
	}
	_, ok := v.tools[name]
	return ok
}

func (v *Validator) Check(name, argsJSON string) Verdict {
	if strings.TrimSpace(argsJSON) == "" {
		argsJSON = "{}"
	}

	args := argsJSON
	var actions []string

	if !gjson.Valid(args) {
		fixed, fixActions := repairJSON(args)
		actions = append(actions, fixActions...)
		if fixed == "" || !gjson.Valid(fixed) {
			return Verdict{
				Args: "{}",
				Issue: &Issue{
					ToolName: name,
					Bucket:   BucketInvalidJSON,
					Detail:   truncateDetail("unparseable tool arguments"),
					Actions:  append(actions, "empty_object_fallback"),
				},
			}
		}
		args = fixed
	}
	jsonRepaired := len(actions) > 0

	if v == nil {
		return verdictAfterValidation(name, args, jsonRepaired, actions, nil)
	}

	ts, known := v.tools[name]
	if !known {
		return Verdict{
			Args: args,
			Issue: &Issue{
				ToolName: name,
				Bucket:   BucketUnknownTool,
				Detail:   truncateDetail("tool not present in request tool set"),
				Repaired: false,
				Actions:  actions,
			},
		}
	}

	args, normActions := normalizeArgs(args, ts.required)
	actions = append(actions, normActions...)

	if ts.compiled == nil {
		return verdictAfterValidation(name, args, jsonRepaired, actions, nil)
	}

	verr := validate(ts.compiled, args)
	if verr == nil {
		return verdictAfterValidation(name, args, jsonRepaired, actions, nil)
	}

	repaired, repairActions := repairArgs(ts.compiled, args, verr)
	if len(repairActions) > 0 {
		if rerr := validate(ts.compiled, repaired); rerr == nil {
			return Verdict{
				Args: repaired,
				Issue: &Issue{
					ToolName: name,
					Bucket:   BucketSchemaMismatch,
					Detail:   detailFromError(verr),
					Repaired: true,
					Actions:  append(actions, repairActions...),
				},
			}
		}
	}
	return Verdict{
		Args: args,
		Issue: &Issue{
			ToolName: name,
			Bucket:   BucketSchemaMismatch,
			Detail:   detailFromError(verr),
			Repaired: false,
			Actions:  actions,
		},
	}
}

func verdictAfterValidation(name, args string, jsonRepaired bool, actions []string, _ error) Verdict {
	if !jsonRepaired {
		return Verdict{OK: true, Args: args}
	}
	return Verdict{
		Args: args,
		Issue: &Issue{
			ToolName: name,
			Bucket:   BucketInvalidJSON,
			Detail:   truncateDetail("malformed tool argument JSON (repaired)"),
			Repaired: true,
			Actions:  actions,
		},
	}
}

func validate(schema *jsonschema.Schema, args string) (verr error) {
	defer func() {
		if r := recover(); r != nil {
			observability.Get().Warn("toolcheck: validate panic", "panic", fmt.Sprint(r))
			verr = nil
		}
	}()
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(args))
	if err != nil {
		return nil
	}
	return schema.Validate(instance)
}

func normalizeArgs(args string, required map[string]struct{}) (out string, actions []string) {
	parsed := gjson.Parse(args)
	if !parsed.IsObject() {
		return args, nil
	}
	type optionalMember struct {
		key       string
		valueType gjson.Type
	}
	var first optionalMember
	firstValueEnd := 0
	firstIsRootMember := false
	var optional []optionalMember
	found := false
	memberCount := 0
	parsed.ForEach(func(key, value gjson.Result) bool {
		memberCount++
		if (value.Type != gjson.String || value.Str != "") && value.Type != gjson.Null {
			return true
		}
		if _, req := required[key.Str]; req {
			return true
		}
		if !found {
			first, found = optionalMember{key.Str, value.Type}, true
			firstValueEnd = value.Index + len(value.Raw)
			firstIsRootMember = memberCount == 1
			return true
		}
		if optional == nil {
			optional = append(optional, first)
		}
		optional = append(optional, optionalMember{key.Str, value.Type})
		return true
	})
	if !found {
		return args, nil
	}
	if optional == nil {
		if len(args) >= 512 && firstIsRootMember && first.key != "" && mutationKey(first.key) == first.key && !argumentLibraryPath([]string{first.key}) {
			out = removeFirstArgumentMember(args, parsed.Index, firstValueEnd)
		} else {
			var err error
			out, err = sjson.Delete(args, argumentMutationPath([]string{first.key}))
			if err != nil {
				return args, nil
			}
		}
		if first.valueType == gjson.String {
			return out, []string{"drop_empty_optional"}
		}
		return out, []string{"drop_null_optional"}
	}
	document := newArgumentDocument(args)
	document.rootMemberCapacity = memberCount
	for _, member := range optional {
		if _, ok := document.delete([]string{member.key}); !ok {
			continue
		}
		if member.valueType == gjson.String {
			actions = append(actions, "drop_empty_optional")
		} else {
			actions = append(actions, "drop_null_optional")
		}
	}
	return document.materialize(), actions
}

func detailFromError(err error) string {
	var verr *jsonschema.ValidationError
	if !errors.As(err, &verr) {
		return truncateDetail(err.Error())
	}
	leaf := firstLeaf(verr)
	path := "/" + strings.Join(leaf.InstanceLocation, "/")
	return truncateDetail(path + ": " + leaf.ErrorKind.LocalizedString(localePrinter))
}

func truncateDetail(s string) string {
	return observability.Preview(s, maxDetailBytes)
}

func firstLeaf(verr *jsonschema.ValidationError) *jsonschema.ValidationError {
	for len(verr.Causes) > 0 {
		verr = verr.Causes[0]
	}
	return verr
}
