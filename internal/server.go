package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nar-lang/nar-common/ast"
	"github.com/nar-lang/nar-common/ast/normalized"
	"github.com/nar-lang/nar-common/ast/parsed"
	"github.com/nar-lang/nar-common/ast/typed"
	"github.com/nar-lang/nar-common/logger"
	locator2 "github.com/nar-lang/nar-compiler/locator"
	"github.com/nar-lang/nar-lsp/internal/protocol"
	"log"
	"os"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
)

const Version = 100

type server struct {
	id        int
	log       *logger.LogWriter
	trace     protocol.TraceValues
	cancelCtx context.CancelFunc

	rootURI          protocol.DocumentURI
	initialized      bool
	responseChan     chan rpcResponse
	notificationChan chan rpcNotification
	inChan           chan []byte
	compileChan      chan docChange
	compiledChan     chan struct{}
	locker           sync.Locker

	documentToPackageRoot map[protocol.DocumentURI]string
	packageRootToName     map[string]ast.PackageIdentifier
	locator               locator2.Locator
	provides              map[string]*provider
	cacheProvider         locator2.Provider
	workspaceProviders    []locator2.Provider
	parsedModules         map[ast.QualifiedIdentifier]*parsed.Module
	normalizedModules     map[ast.QualifiedIdentifier]*normalized.Module
	typedModules          map[ast.QualifiedIdentifier]*typed.Module
	openedDocuments       map[protocol.DocumentURI]struct{}
}

type docChange struct {
	uri   protocol.DocumentURI
	force bool
}

type LanguageServer interface {
	Close()
	GotMessage(msg []byte)
}

var lastId = 0

func NewServer(cacheDir string, writeResponse func([]byte)) LanguageServer {
	lastId++
	ctx, cancelCtx := context.WithCancel(context.Background())
	s := &server{
		id:               lastId,
		cancelCtx:        cancelCtx,
		inChan:           make(chan []byte, 16),
		responseChan:     make(chan rpcResponse, 16),
		notificationChan: make(chan rpcNotification, 128),
		compileChan:      make(chan docChange, 1024),
		compiledChan:     make(chan struct{}),
		locker:           &sync.Mutex{},

		log:                   &logger.LogWriter{},
		cacheProvider:         locator2.NewDirectoryProvider(cacheDir),
		documentToPackageRoot: map[protocol.DocumentURI]string{},
		packageRootToName:     map[string]ast.PackageIdentifier{},
		provides:              map[string]*provider{},
		parsedModules:         map[ast.QualifiedIdentifier]*parsed.Module{},
		normalizedModules:     map[ast.QualifiedIdentifier]*normalized.Module{},
		typedModules:          map[ast.QualifiedIdentifier]*typed.Module{},
		openedDocuments:       map[protocol.DocumentURI]struct{}{},
	}
	go s.sender(writeResponse, ctx)
	go s.receiver(ctx)
	go s.compiler(ctx)
	return s
}
func (s *server) Close() {
	s.cancelCtx()
	close(s.responseChan)
	close(s.notificationChan)
	close(s.inChan)
	close(s.compileChan)
}

func (s *server) GotMessage(msg []byte) {
	s.inChan <- msg
}

func (s *server) receiver(ctx context.Context) {
	for {
		select {
		case msg := <-s.inChan:
			if err := s.handleMessage(msg); err != nil {
				log.Println(err.Error())
			}
			break
		case <-ctx.Done():
			return
		}
	}
}

func (s *server) sender(writeResponse func([]byte), ctx context.Context) {
	for {
		var data []byte
		var err error
		select {
		case response := <-s.responseChan:
			data, err = json.Marshal(response)
			break
		case notification := <-s.notificationChan:
			data, err = json.Marshal(notification)
			break
		case <-ctx.Done():
			return
		}
		if err != nil {
			s.log.Err(err)
		} else {
			writeResponse(data)
		}
		s.log.Flush(os.Stdout)
	}
}

func (s *server) handleMessage(msg []byte) error {
	defer func() {
		if r := recover(); r != nil {
			s.reportError(fmt.Sprintf("internal error:\n%v\n\n%s", r, debug.Stack()))
		}
	}()

	var call rpcCall
	if err := json.Unmarshal(msg, &call); nil != err {
		return err
	}

	println("<- " + call.Method)

	response := rpcResponse{
		Jsonrpc: "2.0",
		Id:      call.Id,
		Error:   nil,
		Result:  []byte("null"),
	}
	needResponse := true

	v := reflect.ValueOf(s)
	methodName := strings.ReplaceAll(call.Method, "$", "S")
	methodName = strings.ReplaceAll(methodName, "/", "_")
	methodName = strings.ToUpper(methodName[0:1]) + methodName[1:]
	fn := v.MethodByName(methodName)
	if fn.IsValid() {
		paramType := fn.Type().In(0).Elem()
		param := reflect.New(paramType)
		paramIface := param.Interface()
		if err := json.Unmarshal(call.Params, paramIface); nil != err {
			return err
		}
		results := fn.Call([]reflect.Value{param})

		if len(results) == 1 {
			needResponse = false
			err, _ := results[0].Interface().(error)
			if nil != err {
				return err
			}
		} else {
			err, _ := results[1].Interface().(error)

			if nil == err {
				var result []byte
				if result, err = json.Marshal(results[0].Interface()); err == nil {
					response.Result = result
				}
			}
			if nil != err {
				response.Error = &rpcError{
					Code:    rpcInternalError,
					Message: err.Error(),
				}
			}
		}
	} else {
		response.Error = &rpcError{
			Code:    rpcMethodNotFound,
			Message: fmt.Sprintf("Method %s not implemented", call.Method),
		}
	}
	if needResponse {
		s.responseChan <- response
	}
	return nil
}

func (s *server) notify(message string, params any) {
	println("-> " + message)

	if data, err := json.Marshal(params); err == nil {
		s.notificationChan <- rpcNotification{
			Jsonrpc: "2.0",
			Method:  message,
			Params:  data,
		}
	}
}

func (s *server) reportError(message string) {
	s.notify("window/showMessage", protocol.ShowMessageParams{
		Type:    protocol.Error,
		Message: message,
	})
	fmt.Println(message)
}
