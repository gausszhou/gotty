package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gausszhou/gotty/internal/utils"
)

// AttachFlags registers one flag per option-struct field that carries a
// `flagName` tag. Flag defaults come from the current field values, so
// ApplyDefaultValues must run before AttachFlags.
func AttachFlags(cmd *cobra.Command, options ...interface{}) (mappings map[string]string, err error) {
	mappings = make(map[string]string)

	for _, struct_ := range options {
		val := reflect.ValueOf(struct_).Elem()
		typ := val.Type()

		for i := 0; i < val.NumField(); i++ {
			field := val.Field(i)
			fieldType := typ.Field(i)

			flagName := fieldType.Tag.Get("flagName")
			if flagName == "" {
				continue
			}
			flagShortName := fieldType.Tag.Get("flagSName")
			flagDescription := fieldType.Tag.Get("flagDescribe")
			mappings[flagName] = fieldType.Name

			flags := cmd.Flags()
			switch field.Kind() {
			case reflect.String:
				if flagShortName != "" {
					flags.StringP(flagName, flagShortName, field.Interface().(string), flagDescription)
				} else {
					flags.String(flagName, field.Interface().(string), flagDescription)
				}
			case reflect.Bool:
				if flagShortName != "" {
					flags.BoolP(flagName, flagShortName, field.Interface().(bool), flagDescription)
				} else {
					flags.Bool(flagName, field.Interface().(bool), flagDescription)
				}
			case reflect.Int:
				if flagShortName != "" {
					flags.IntP(flagName, flagShortName, field.Interface().(int), flagDescription)
				} else {
					flags.Int(flagName, field.Interface().(int), flagDescription)
				}
			}
		}
	}

	return
}

// ApplyFlags writes explicitly-set CLI flags back into the option structs.
func ApplyFlags(cmd *cobra.Command, mappings map[string]string, options ...interface{}) {
	for flagName, fieldName := range mappings {
		if !cmd.Flags().Changed(flagName) {
			continue
		}

		for _, struct_ := range options {
			val := reflect.ValueOf(struct_).Elem()
			typ := val.Type()

			for i := 0; i < val.NumField(); i++ {
				fieldType := typ.Field(i)
				if fieldType.Name != fieldName {
					continue
				}

				field := val.Field(i)
				var v reflect.Value
				switch field.Kind() {
				case reflect.String:
					v = reflect.ValueOf(cmd.Flags().Lookup(flagName).Value.String())
				case reflect.Bool:
					v = reflect.ValueOf(cmd.Flags().Lookup(flagName).Value.String() == "true")
				case reflect.Int:
					intVal, _ := strconv.Atoi(cmd.Flags().Lookup(flagName).Value.String())
					v = reflect.ValueOf(intVal)
				}
				field.Set(v)
			}
		}
	}
}

// ApplyEnv fills flags that were not explicitly set from GOTTY_* env vars,
// e.g. GOTTY_PORT for the flag --port.
func ApplyEnv(cmd *cobra.Command, mappings map[string]string, options ...interface{}) error {
	for flagName, fieldName := range mappings {
		if cmd.Flags().Changed(flagName) {
			continue
		}

		envName := "GOTTY_" + strings.ToUpper(strings.ReplaceAll(flagName, "-", "_"))
		raw, ok := os.LookupEnv(envName)
		if !ok {
			continue
		}

		for _, struct_ := range options {
			val := reflect.ValueOf(struct_).Elem()
			typ := val.Type()

			for i := 0; i < val.NumField(); i++ {
				fieldType := typ.Field(i)
				if fieldType.Name != fieldName {
					continue
				}

				field := val.Field(i)
				var v reflect.Value
				switch field.Kind() {
				case reflect.String:
					v = reflect.ValueOf(raw)
				case reflect.Bool:
					b, err := strconv.ParseBool(raw)
					if err != nil {
						return fmt.Errorf("invalid bool value for env %s: %s", envName, raw)
					}
					v = reflect.ValueOf(b)
				case reflect.Int:
					n, err := strconv.Atoi(raw)
					if err != nil {
						return fmt.Errorf("invalid int value for env %s: %s", envName, raw)
					}
					v = reflect.ValueOf(n)
				}
				field.Set(v)
			}
		}
	}

	return nil
}

// ApplyConfigFile loads a JSON config file into the option structs.
// Unknown keys are ignored, so files written for the previous GoTTY
// generations keep working. An empty file is accepted as a no-op.
func ApplyConfigFile(filePath string, options ...interface{}) error {
	filePath = utils.Expand(filePath)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return err
	}

	log.Printf("Loading config file at: %s", filePath)
	fileData, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(fileData))) == 0 {
		return nil
	}

	for _, object := range options {
		if err := json.Unmarshal(fileData, object); err != nil {
			return err
		}
	}

	return nil
}
