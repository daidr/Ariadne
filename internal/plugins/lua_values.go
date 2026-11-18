package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	lua "github.com/yuin/gopher-lua"
)

const maxLuaValue = 8 << 20
const maxLuaNodes = 100_000

type valueBudget struct{ bytes, nodes int }

func (budget *valueBudget) take(bytes, depth int) error {
	budget.bytes += bytes
	budget.nodes++
	if budget.bytes > maxLuaValue || budget.nodes > maxLuaNodes || depth > 48 {
		return errors.New("plugin value exceeds size or nesting limit")
	}
	return nil
}

type luaValues struct {
	state *lua.LState
	array *lua.LTable
	null  *lua.LUserData
}

func (values luaValues) fromJSON(data []byte) (lua.LValue, error) {
	if len(data) > maxLuaValue {
		return nil, errors.New("JSON value exceeds 8 MiB")
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return values.fromGo(value, &valueBudget{}, 0)
}

func (values luaValues) fromGo(value any, budget *valueBudget, depth int) (lua.LValue, error) {
	if err := budget.take(0, depth); err != nil {
		return nil, err
	}
	switch value := value.(type) {
	case nil:
		return values.null, nil
	case string:
		return lua.LString(value), nil
	case bool:
		return lua.LBool(value), nil
	case float64:
		return lua.LNumber(value), nil
	case []any:
		out := values.state.CreateTable(len(value), 0)
		out.Metatable = values.array
		for _, item := range value {
			child, err := values.fromGo(item, budget, depth+1)
			if err != nil {
				return nil, err
			}
			out.Append(child)
		}
		return out, nil
	case map[string]any:
		out := values.state.CreateTable(0, len(value))
		for key, item := range value {
			child, err := values.fromGo(item, budget, depth+1)
			if err != nil {
				return nil, err
			}
			out.RawSetString(key, child)
		}
		return out, nil
	default:
		return nil, errors.New("invalid JSON value")
	}
}

func (values luaValues) toGo(value lua.LValue, budget *valueBudget, depth int) (any, error) {
	if err := budget.take(1, depth); err != nil {
		return nil, err
	}
	if value == lua.LNil || value == values.null {
		return nil, nil
	}
	switch value := value.(type) {
	case lua.LString:
		return string(value), budget.take(len(value), depth)
	case lua.LBool:
		return bool(value), nil
	case lua.LNumber:
		n := float64(value)
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			return n, nil
		}
	case *lua.LTable:
		object := map[string]any{}
		list := map[int]any{}
		var problem error
		value.ForEach(func(key, item lua.LValue) {
			if problem != nil {
				return
			}
			var child any
			child, problem = values.toGo(item, budget, depth+1)
			if problem != nil {
				return
			}
			switch key := key.(type) {
			case lua.LString:
				problem = budget.take(len(key), depth)
				object[string(key)] = child
			case lua.LNumber:
				if key >= 1 && key <= maxLuaNodes && key == lua.LNumber(int(key)) {
					list[int(key)] = child
				} else {
					problem = errors.New("plugin array indices must be positive consecutive integers")
				}
			default:
				problem = errors.New("plugin object keys must be strings")
			}
		})
		if problem != nil {
			return nil, problem
		}
		if len(list) == 0 && value.Metatable != values.array {
			return object, nil
		}
		if len(object) != 0 {
			return nil, errors.New("plugin table mixes array indices and object keys")
		}
		out := make([]any, len(list))
		for index, item := range list {
			if index > len(out) {
				return nil, errors.New("plugin array has missing entries")
			}
			out[index-1] = item
		}
		return out, nil
	}
	return nil, fmt.Errorf("cannot convert Lua %s to JSON", value.Type())
}

func (values luaValues) encode(value lua.LValue) ([]byte, error) {
	object, err := values.toGo(value, &valueBudget{}, 0)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(object)
	if len(data) > maxLuaValue {
		return nil, errors.New("plugin JSON value exceeds 8 MiB")
	}
	return data, err
}

func (values luaValues) decode(value lua.LValue, out any) error {
	data, err := values.encode(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
