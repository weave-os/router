package translate

import (
	"reflect"
	"regexp"
	"strings"
)

func detachFromBody[T any](value T) T {
	detachStrings(reflect.ValueOf(&value).Elem())
	return value
}

func detachStrings(value reflect.Value) {
	switch value.Kind() {
	case reflect.String:
		if value.CanSet() {
			value.SetString(strings.Clone(value.String()))
		}
	case reflect.Struct:
		for i := range value.NumField() {
			detachStrings(value.Field(i))
		}
	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return
		}
		for i := range value.Len() {
			detachStrings(value.Index(i))
		}
	case reflect.Pointer:
		if !value.IsNil() {
			detachStrings(value.Elem())
		}
	case reflect.Map:
		detachMap(value)
	default:
	}
}

func detachMap(value reflect.Value) {
	if value.IsNil() || !value.CanSet() {
		return
	}
	detached := reflect.MakeMapWithSize(value.Type(), value.Len())
	entries := value.MapRange()
	for entries.Next() {
		key := reflect.New(value.Type().Key()).Elem()
		key.Set(entries.Key())
		detachStrings(key)
		element := reflect.New(value.Type().Elem()).Elem()
		element.Set(entries.Value())
		detachStrings(element)
		detached.SetMapIndex(key, element)
	}
	value.Set(detached)
}

func CodexSubagentPath(body []byte) string {
	return strings.Clone(codexSubagentPathFromView(body))
}

func (e *RequestEnvelope) FirstUserMessageText() string {
	return strings.Clone(e.firstUserMessageTextFromView())
}

func LastAnnouncedMessagesModel(body []byte) string {
	return strings.Clone(lastAnnouncedMessagesModelFromView(body))
}

func lastAnnouncedResponsesModel(body []byte, pattern *regexp.Regexp) string {
	return strings.Clone(lastAnnouncedResponsesModelFromView(body, pattern))
}

func openAISystemText(body []byte) string {
	return strings.Clone(openAISystemTextFromView(body))
}

func openAILastUserMessage(body []byte) LastUserMessageInfo {
	return detachFromBody(openAILastUserMessageFromView(body))
}

func (e *RequestEnvelope) openAIRoutingFeatures() RoutingFeatures {
	feats := e.openAIRoutingFeaturesFromView()
	feats.Model = strings.Clone(feats.Model)
	feats.LastPreview = strings.Clone(feats.LastPreview)
	return feats
}

func (e *RequestEnvelope) openAIConversationMessages() []ConversationMessage {
	return detachFromBody(e.openAIConversationMessagesFromView())
}

func openAIAssistantToolCallEntries(body []byte) []assistantToolCallEntry {
	return detachFromBody(openAIAssistantToolCallEntriesFromView(body))
}

func convertPortableCodexResponses(body []byte) (ResponsesConversion, error) {
	conversion, err := convertPortableCodexResponsesFromView(body)
	return detachFromBody(conversion), err
}
