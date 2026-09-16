package auth

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// MetadataStorageCloner isolates mutable token storage before an operator edit.
// Login-only guards and frozen acquisition settings must not survive the clone.
type MetadataStorageCloner interface {
	CloneForMetadataUpdate() (TokenStorage, error)
}

// CloneMetadataForUpdate copies JSON-compatible metadata without sharing maps or
// slices. It preserves numeric Go types used by three-way metadata comparisons.
func CloneMetadataForUpdate(metadata map[string]any) (map[string]any, error) {
	if metadata == nil {
		return nil, nil
	}
	if _, errMarshal := json.Marshal(metadata); errMarshal != nil {
		return nil, fmt.Errorf("clone credential metadata: %w", errMarshal)
	}
	return cloneMetadataUpdateValue(reflect.ValueOf(metadata)).Interface().(map[string]any), nil
}

// cloneMetadataUpdateValue is limited to metadata already validated for JSON
// persistence; native token storage uses explicit provider-specific copies.
func cloneMetadataUpdateValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface, reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		if value.Kind() == reflect.Pointer {
			cloned := reflect.New(value.Type().Elem())
			cloned.Elem().Set(cloneMetadataUpdateValue(value.Elem()))
			return cloned
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneMetadataUpdateValue(value.Elem()))
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(iter.Key(), cloneMetadataUpdateValue(iter.Value()))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := range value.Len() {
			cloned.Index(index).Set(cloneMetadataUpdateValue(value.Index(index)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			cloned.Index(index).Set(cloneMetadataUpdateValue(value.Index(index)))
		}
		return cloned
	case reflect.Struct:
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(value)
		for index := range value.NumField() {
			if cloned.Field(index).CanSet() && value.Field(index).CanInterface() {
				cloned.Field(index).Set(cloneMetadataUpdateValue(value.Field(index)))
			}
		}
		return cloned
	default:
		return value
	}
}
