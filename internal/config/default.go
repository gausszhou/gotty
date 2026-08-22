package config

import (
	"fmt"
	"reflect"
	"strconv"
)

func ApplyDefaultValues(struct_ interface{}) (err error) {
	val := reflect.ValueOf(struct_).Elem()
	typ := val.Type()

	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := typ.Field(i)

		defaultValue := fieldType.Tag.Get("default")
		if defaultValue == "" {
			continue
		}

		var val reflect.Value
		switch field.Kind() {
		case reflect.String:
			val = reflect.ValueOf(defaultValue)
		case reflect.Bool:
			if defaultValue == "true" {
				val = reflect.ValueOf(true)
			} else if defaultValue == "false" {
				val = reflect.ValueOf(false)
			} else {
				return fmt.Errorf("invalid bool expression: %v, use true/false", defaultValue)
			}
		case reflect.Int:
			intVal, err := strconv.Atoi(defaultValue)
			if err != nil {
				return err
			}
			val = reflect.ValueOf(intVal)
		default:
			val = field
		}
		field.Set(val)
	}
	return nil
}
