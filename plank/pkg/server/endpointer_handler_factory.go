package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/pb33f/ranch/model"
	"github.com/pb33f/ranch/service"
	"net/http"
	"reflect"
	"time"
)

// buildEndpointHandler builds a http.HandlerFunc that wraps Transport Bus operations in an HTTP request-response cycle.
// service channel, request builder and rest bridge timeout are passed as parameters.
func (ps *platformServer) buildEndpointHandler(svcChannel string, reqBuilder service.RequestBuilder, restBridgeTimeout time.Duration, msgChan chan *model.Message) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if r := recover(); r != nil {
				ps.serverConfig.Logger.Error(fmt.Sprint(r))
				http.Error(w, "Internal Server Error", 500)
			}
		}()

		// set context that expires after the provided amount of time in restBridgeTimeout to prevent requests from hanging forever
		ctx, cancelFn := context.WithTimeout(context.Background(), restBridgeTimeout)
		defer cancelFn()

		// relay the request to transport channel
		reqModel := reqBuilder(w, r)
		err := ps.eventbus.SendRequestMessage(svcChannel, reqModel, reqModel.Id)

		// get a response from the channel, render the results using ResponseWriter and log the data/error
		// to the console as well.
		select {
		case <-ctx.Done():
			http.Error(
				w,
				fmt.Sprintf("no response received from service channel in %s, request timed out", restBridgeTimeout.String()), 500)
		case msg := <-msgChan:
			if msg.Error != nil {
				ps.serverConfig.Logger.Error(
					"Error received from channel", "error", msg.Error, "channel", svcChannel)
				http.Error(w, msg.Error.Error(), 500)
			} else {
				// only send the actual user payloadChannel not wrapper information
				response := msg.Payload.(*model.Response)
				var respBody interface{}
				if response.Error {
					if response.Payload != nil {
						respBody = response.Payload
					} else {
						respBody = response
					}
				} else {
					respBody = response.Payload
				}

				// if our Message is an error and it has a code, lets send that back to the client.
				if response.Error {

					// we have to set the headers for the error response
					for k, v := range response.Headers {
						w.Header().Set(k, fmt.Sprint(v))
					}

					// deal with the response body now, if set.
					if respBody != "" {
						w.WriteHeader(response.ErrorCode)
						switch reflect.TypeOf(respBody).Kind() {
						case reflect.String:
							_, _ = w.Write([]byte(fmt.Sprint(respBody)))
						case reflect.Pointer:
							n, e := json.Marshal(respBody)
							if e != nil {
								http.Error(w, e.Error(), 500)
							} else {
								_, _ = w.Write(n)
							}
						}
					} else {
						if response.ErrorObject != nil {
							n, e := json.Marshal(respBody)
							if e != nil {
								http.Error(w, e.Error(), 500)
							} else {
								w.WriteHeader(response.ErrorCode)
								w.Write(n)
							}
						}
					}
				} else {
					// if the response has headers, set those headers. particularly if you're sending around
					// byte array data for things like zip files etc.
					for k, v := range response.Headers {
						w.Header().Set(k, fmt.Sprint(v))
					}

					var respBodyBytes []byte
					// ensure respBody is properly converted to a byte slice as Content-Type header might not be
					// set in the request and the restBody could be in a format that is not a byte slice.
					if response.Marshal {
						respBodyBytes, err = ensureResponseInByteSlice(respBody)
					} else {
						respBodyBytes = []byte(fmt.Sprint(respBody))
					}

					if response.HttpStatusCode != 0 {
						w.WriteHeader(response.HttpStatusCode)
					}

					// write the non-error payload back.
					if _, err = w.Write(respBodyBytes); err != nil {
						ps.serverConfig.Logger.Error("error received from channel", "error",
							err.Error(), "channel", svcChannel)
						http.Error(w, err.Error(), 500)
					}
				}
			}
		}
	}
}
