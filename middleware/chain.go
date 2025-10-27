package middleware

import (
	"context"
	"log"
	"naevis/db"
	"net/http"

	"github.com/julienschmidt/httprouter"
	"go.mongodb.org/mongo-driver/mongo"
)

// Middleware signature for httprouter handlers
type Middleware func(httprouter.Handle) httprouter.Handle

// Chain composes middlewares left-to-right
func Chain(middlewares ...Middleware) Middleware {
	return func(final httprouter.Handle) httprouter.Handle {
		for i := len(middlewares) - 1; i >= 0; i-- {
			final = middlewares[i](final)
		}
		return final
	}
}

// TxnKey is the context key where the session is stored
type txnKey struct{}

// WithTxn injects a MongoDB session into the request context
func WithTxn(next httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
		client := db.Client

		// Start a new session
		session, err := client.StartSession()
		if err != nil {
			http.Error(w, "failed to start db session", http.StatusInternalServerError)
			return
		}
		defer session.EndSession(r.Context())

		err = mongo.WithSession(r.Context(), session, func(sc mongo.SessionContext) error {
			if err := session.StartTransaction(); err != nil {
				return err
			}

			// Add txn session into request context
			ctx := context.WithValue(r.Context(), txnKey{}, sc)
			reqWithCtx := r.WithContext(ctx)

			// Call handler
			next(w, reqWithCtx, ps)

			// If handler wrote an error, rollback
			if rw, ok := w.(*ResponseWriterWithStatus); ok && rw.status >= 400 {
				_ = session.AbortTransaction(sc)
				return nil
			}

			// Commit transaction
			if err := session.CommitTransaction(sc); err != nil {
				log.Printf("commit error: %v", err)
				return err
			}
			return nil
		})

		if err != nil {
			http.Error(w, "transaction failed: "+err.Error(), http.StatusInternalServerError)
		}
	}
}

// GetTxn extracts session context if inside txn
func GetTxn(ctx context.Context) (mongo.SessionContext, bool) {
	sc, ok := ctx.Value(txnKey{}).(mongo.SessionContext)
	return sc, ok
}

// ResponseWriterWithStatus wraps http.ResponseWriter to capture status codes
type ResponseWriterWithStatus struct {
	http.ResponseWriter
	status int
}

func (rw *ResponseWriterWithStatus) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// WrapResponseWriter ensures we can capture handler’s response status
func WrapResponseWriter(w http.ResponseWriter) *ResponseWriterWithStatus {
	return &ResponseWriterWithStatus{ResponseWriter: w, status: 200}
}
