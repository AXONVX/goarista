// Copyright (c) 2016 Arista Networks, Inc.
// Use of this source code is governed by the Apache License 2.0
// that can be found in the COPYING file.

package gnmi

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/aristanetworks/goarista/elasticsearch"
	"github.com/aristanetworks/goarista/kafka"

	"github.com/Shopify/sarama"
	"github.com/aristanetworks/glog"
	"github.com/golang/protobuf/proto"
	"github.com/openconfig/gnmi/proto/gnmi"
)

// UnhandledMessageError is used for proto messages not matching the handled types
type UnhandledMessageError struct {
	message proto.Message
}

func (e UnhandledMessageError) Error() string {
	return fmt.Sprintf("Unexpected type %T in proto message: %#v", e.message, e.message)
}

// UnhandledSubscribeResponseError is used for subscribe responses not matching the handled types
type UnhandledSubscribeResponseError struct {
	response *gnmi.SubscribeResponse
}

func (e UnhandledSubscribeResponseError) Error() string {
	return fmt.Sprintf("Unexpected type %T in subscribe response: %#v", e.response, e.response)
}

type elasticsearchMessageEncoder struct {
	*kafka.BaseEncoder
	topic   string
	dataset string
	key     sarama.Encoder
}

// NewEncoder creates and returns a new elasticsearch MessageEncoder
func NewEncoder(topic string, key sarama.Encoder, dataset string) kafka.MessageEncoder {
	baseEncoder := kafka.NewBaseEncoder("elasticsearch")
	return &elasticsearchMessageEncoder{
		BaseEncoder: baseEncoder,
		topic:       topic,
		dataset:     dataset,
		key:         key,
	}
}

// Split a timestamp into seconds and nanoseconds components - Max slicing length is 10
func splitTimestamp(timestamp uint64, lenSlice int) (int64, int64, error) {
	var secTimestamp int64		// Note max secTimestamp length is 10
	var nsecTimestamp int64		// Note max secTimestamp length is 9
	var err error

	timestampString := strconv.FormatUint(timestamp, 10)
	lenTimestamp := len(timestampString)

	if lenSlice > lenTimestamp || lenSlice > 10 {
		return 0, 0, fmt.Errorf("Slicing length is greater than the timestamp length or 10")
	} else if lenTimestamp > 19 {
		return 0, 0, fmt.Errorf("Timestamp length is too long (Greater than 19)")
	}

	secTimestamp, err = strconv.ParseInt(timestampString[:lenSlice], 10, 0)
	if err != nil {
		return 0, 0, err
	}
	if lenTimestamp > 10 && lenSlice == 10 {
		lenSliceNew := len(timestampString)-lenSlice
		nsecTimestamp, err = strconv.ParseInt(timestampString[len(timestampString)-lenSliceNew:], 10, 0)
		if err != nil {
			return 0, 0, err
		}
	} else {
		nsecTimestamp = 0
	}
	return secTimestamp, nsecTimestamp, err
}

func (e *elasticsearchMessageEncoder) Encode(message proto.Message) ([]*sarama.ProducerMessage, error) {
	response, ok := message.(*gnmi.SubscribeResponse)
	if !ok {
		return nil, UnhandledMessageError{message: message}
	}
	update := response.GetUpdate()
	if update == nil {
		return nil, UnhandledSubscribeResponseError{response: response}
	}
	updateMaps, err := elasticsearch.NotificationToMaps(e.dataset, update)
	if err != nil {
		return nil, err
	}
	messages := make([]*sarama.ProducerMessage, len(updateMaps))
	for i, updateMap := range updateMaps {
		updateJSON, err := json.Marshal(updateMap)
		if err != nil {
			return nil, err
		}
		glog.V(9).Infof("kafka: %s", updateJSON)

		// Derive the timestamp value within sarama.ProducerMessage (Either timestamp collected from message or system derived)
		var kafkaTimestamp time.Time
		if timestamp, ok := updateMap["Timestamp"]; ok {
			secTimestamp, nsecTimestamp, err := splitTimestamp(timestamp.(uint64), 10)
			if err != nil {
				kafkaTimestamp = time.Now()
				glog.V(9).Infof("Using system time for Timestamp")
			} else {
				kafkaTimestamp = time.Unix(secTimestamp, nsecTimestamp)
				glog.V(9).Infof("Using message embedded time for Timestamp")
			}
		} else {
			kafkaTimestamp = time.Now()
			glog.V(9).Infof("Using system time for Timestamp")
		}
		glog.V(9).Infof("timestamp: %s", kafkaTimestamp)

		messages[i] = &sarama.ProducerMessage {
			Topic:    e.topic,
			Key:      e.key,
			Value:    sarama.ByteEncoder(updateJSON),
			Metadata: kafka.Metadata{StartTime: time.Unix(0, update.Timestamp), NumMessages: 1},
			Timestamp: kafkaTimestamp,
		}
	}
	return messages, nil
}
