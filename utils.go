package psip

import (
	"fmt"

	"github.com/emiago/sipgo/sip"
)

func ReadHeaderByType[T sip.Header](m sip.Message, name string) ([]T, error) {
	hdrs := m.GetHeaders(name)
	ret := make([]T, 0, len(hdrs))
	for _, h := range hdrs {
		hh, ok := h.(T)
		if !ok {
			return ret, fmt.Errorf("fail to cast %v", h)
		}
		ret = append(ret, hh)
	}
	return ret, nil
}
