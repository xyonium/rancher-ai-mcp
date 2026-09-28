package dispatch

import (
	"fmt"
	"reflect"
	"strings"
)

// Validate checks that every json field name in required is non-zero in p.
// The error names the discriminator value so the caller can self-correct.
func Validate[P any](discriminator, value string, p P, required []string) error {
	if len(required) == 0 {
		return nil
	}
	rt := reflect.TypeOf(p)
	rv := reflect.ValueOf(p)
	byJSONName := make(map[string]int, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		byJSONName[name] = i
	}
	var missing []string
	for _, name := range required {
		idx, ok := byJSONName[name]
		if !ok {
			return fmt.Errorf("dispatch: unknown required field %q (programming error)", name)
		}
		if rv.Field(idx).IsZero() {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s=%q is missing required parameter(s): %s", discriminator, value, strings.Join(missing, ", "))
	}
	return nil
}
