package gateway

import (
	"errors"
	"net/http"
	"time"

	"github.com/getkin/kin-openapi/routers"

	"github.com/TykTechnologies/tyk/apidef"
	"github.com/TykTechnologies/tyk/apidef/oas"
	"github.com/TykTechnologies/tyk/internal/otel"
	"github.com/TykTechnologies/tyk/request"
)

const XTykAPIExpires = "x-tyk-api-expires"

// VersionCheck will check whether the version of the requested API the request is accessing has any restrictions on URL endpoints
type VersionCheck struct {
	BaseMiddleware
	sh SuccessHandler
}

func (v *VersionCheck) Init() {
	v.sh = SuccessHandler{v.BaseMiddleware}
}

func (v *VersionCheck) Name() string {
	return "VersionCheck"
}

func (v *VersionCheck) DoMockReply(w http.ResponseWriter, meta apidef.MockResponseMeta) {
	responseMessage := []byte(meta.Body)
	for header, value := range meta.Headers {
		w.Header().Add(header, value)
	}

	w.WriteHeader(meta.Code)
	w.Write(responseMessage)
}

type Operation struct {
	*oas.Operation
	route      *routers.Route
	pathParams map[string]string
}

func findRouteAndOperation(spec *APISpec, r *http.Request) {
	route, pathParams, err := spec.OASRouter.FindRoute(r)
	if err != nil {
		return
	}

	operation, ok := spec.OAS.GetTykExtension().Middleware.Operations[route.Operation.OperationID]
	if !ok {
		return
	}

	ctxSetOperation(r, &Operation{Operation: operation, route: route, pathParams: pathParams})
}

// ProcessRequest will run any checks on the request on the way through the system, return an error to have the chain fail
func (v *VersionCheck) ProcessRequest(w http.ResponseWriter, r *http.Request, _ interface{}) (error, int) {
	targetVersion := v.Spec.getVersionFromRequest(r)
	if targetVersion == "" {
		targetVersion = v.Spec.VersionDefinition.Default
	}

	ctxSetSpanAttributes(r, v.Name(), otel.APIVersionAttribute(targetVersion))

	if v.Spec.VersionDefinition.Enabled && targetVersion != apidef.Self && targetVersion != v.Spec.VersionDefinition.Name {
		if targetVersion == "" {
			return errors.New(string(VersionNotFound)), http.StatusForbidden
		}

		subVersionID := v.Spec.VersionDefinition.Versions[targetVersion]
		handler, _, found := v.Gw.findInternalHttpHandlerByNameOrID(subVersionID)
		if !found {
			return errors.New(string(VersionDoesNotExist)), http.StatusNotFound
		}

		v.Spec.SanitizeProxyPaths(r)

		handler.ServeHTTP(w, r)
		return nil, mwStatusRespond
	}

	// For OAS route matching
	if v.Spec.HasMock || v.Spec.HasValidateRequest {
		findRouteAndOperation(v.Spec, r)
	}

	// Check versioning, blacklist, whitelist and ignored status
	requestValid, stat := v.Spec.RequestValid(r)
	if !requestValid {
		// Fire a versioning failure event
		v.FireEvent(EventVersionFailure, EventVersionFailureMeta{
			EventMetaDefault: EventMetaDefault{
				Message:            "Attempted access to disallowed version / path.",
				OriginatingRequest: EncodeRequestToEvent(r),
			},
			Path:   r.URL.Path,
			Origin: request.RealIP(r),
			Reason: string(stat),
		})
		return errors.New(string(stat)), http.StatusForbidden
	}

	versionInfo, _ := v.Spec.Version(r)
	versionPaths := v.Spec.RxPaths[versionInfo.Name]
	whiteListStatus := v.Spec.WhiteListEnabled[versionInfo.Name]

	// We handle redirects before ignores in case we aren't using a whitelist
	if stat == StatusRedirectFlowByReply {
		_, meta := v.Spec.URLAllowedAndIgnored(r, versionPaths, whiteListStatus)
		var mockMeta apidef.MockResponseMeta
		var ok bool
		if mockMeta, ok = meta.(apidef.MockResponseMeta); !ok {
			endpointMethodMeta := meta.(*apidef.EndpointMethodMeta)
			mockMeta.Body = endpointMethodMeta.Data
			mockMeta.Headers = endpointMethodMeta.Headers
			mockMeta.Code = endpointMethodMeta.Code
		}

		v.DoMockReply(w, mockMeta)
		return nil, mwStatusRespond
	}

	if !v.Spec.ExpirationTs.IsZero() {
		w.Header().Set(XTykAPIExpires, v.Spec.ExpirationTs.Format(time.RFC1123))
	} else if expTime := versionInfo.ExpiryTime(); !expTime.IsZero() { // Deprecated
		w.Header().Set(XTykAPIExpires, expTime.Format(time.RFC1123))
	}

	if stat == StatusOkAndIgnore {
		ctxSetRequestStatus(r, stat)
	}

	return nil, http.StatusOK
}
