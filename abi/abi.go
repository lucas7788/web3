package abi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/laizy/web3"
	"github.com/laizy/web3/utils"
	"golang.org/x/crypto/sha3"
)

// ABI represents the ethereum abi format
type ABI struct {
	Constructor  *Method
	Methods      map[string]*Method
	MethodsBySig map[string]*Method
	Events       map[string]*Event
}

func (a *ABI) addEvent(e *Event) {
	if len(a.Events) == 0 {
		a.Events = map[string]*Event{}
	}
	a.Events[e.Name] = e
}

func (a *ABI) addMethod(m *Method) {
	if len(a.Methods) == 0 {
		a.Methods = map[string]*Method{}
	}
	a.Methods[m.Name] = m
	// 临时方案：添加完整的sig，避免solidity中重载的函数被覆盖, console合约需要
	if len(a.MethodsBySig) == 0 {
		a.MethodsBySig = map[string]*Method{}
	}
	a.MethodsBySig[m.Sig()] = m
}

// NewABI returns a parsed ABI struct
func NewABI(s string) (*ABI, error) {
	return NewABIFromReader(bytes.NewReader([]byte(s)))
}

// MustNewABI returns a parsed ABI contract or panics if fails
func MustNewABI(s string) *ABI {
	a, err := NewABI(s)
	if err != nil {
		panic(err)
	}
	return a
}

// NewABIFromReader returns an ABI object from a reader
func NewABIFromReader(r io.Reader) (*ABI, error) {
	var abi *ABI
	dec := json.NewDecoder(r)
	if err := dec.Decode(&abi); err != nil {
		return nil, err
	}
	return abi, nil
}

// UnmarshalJSON implements json.Unmarshaler interface
func (a *ABI) UnmarshalJSON(data []byte) error {
	var fields []struct {
		Type            string
		Name            string
		Constant        bool
		Anonymous       bool
		StateMutability string
		Inputs          arguments
		Outputs         arguments
	}

	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	a.Methods = make(map[string]*Method)
	a.Events = make(map[string]*Event)

	for _, field := range fields {
		switch field.Type {
		case "constructor":
			if a.Constructor != nil {
				return fmt.Errorf("multiple constructor declaration")
			}
			a.Constructor = &Method{
				Inputs: field.Inputs.Type(),
			}

		case "function", "":
			c := field.Constant
			if field.StateMutability == "view" || field.StateMutability == "pure" {
				c = true
			}

			m := &Method{
				Name:    field.Name,
				Const:   c,
				Inputs:  field.Inputs.Type(),
				Outputs: field.Outputs.Type(),
			}
			a.addMethod(m)

		case "event":
			a.Events[field.Name] = &Event{
				Name:      field.Name,
				Anonymous: field.Anonymous,
				Inputs:    field.Inputs.Type(),
			}

		case "fallback":
		case "receive":
			// do nothing

		default:
			return fmt.Errorf("unknown field type '%s'", field.Type)
		}
	}
	return nil
}

func (self *ABI) DecodeTxInput(input []byte) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	for _, method := range self.MethodsBySig {
		if bytes.Equal(method.ID(), input[:4]) {
			value, err := Decode(method.Inputs, input[4:])
			if err != nil {
				return nil, err
			}
			result["params"] = value.(map[string]interface{})
			result["method"] = method.DetailedSig()

			return result, nil
		}
	}

	return nil, fmt.Errorf("no matched method")
}

// Method is a callable function in the contract
type Method struct {
	Name    string
	Const   bool
	Inputs  *Type
	Outputs *Type
}

// Sig returns the signature of the method
func (m *Method) Sig() string {
	return buildSignature(m.Name, m.Inputs)
}

func (m *Method) DetailedSig() string {
	return buildHumanSignature(m.Name, m.Inputs)
}

// ID returns the id of the method
func (m *Method) ID() []byte {
	k := acquireKeccak()
	sig := m.Sig()
	k.Write([]byte(sig))
	dst := k.Sum(nil)[:4]
	releaseKeccak(k)
	return dst
}

func NewMethod(name string) (*Method, error) {
	name, inputs, outputs, err := parseMethodSignature(name)
	if err != nil {
		return nil, err
	}
	m := &Method{Name: name, Inputs: inputs, Outputs: outputs}
	return m, nil
}

func MustNewMethod(sig string) *Method {
	m, err := NewMethod(sig)
	if err != nil {
		panic(err)
	}
	return m
}

func (self *Method) EncodeIDAndInput(args ...interface{}) ([]byte, error) {
	data, err := Encode(args, self.Inputs)
	if err != nil {
		return nil, err
	}
	data = append(self.ID(), data...)
	return data, nil
}

func (self *Method) MustEncodeIDAndInput(args ...interface{}) []byte {
	data, err := Encode(args, self.Inputs)
	utils.Ensure(err)
	data = append(self.ID(), data...)
	return data
}

var funcRegexp = regexp.MustCompile("(.*)\\((.*)\\)(.*) returns \\((.*)\\)")

func parseMethodSignature(name string) (string, *Type, *Type, error) {
	name = strings.TrimPrefix(name, "function ")

	matches := funcRegexp.FindAllStringSubmatch(name, -1)
	if len(matches) == 0 {
		return "", nil, nil, fmt.Errorf("no matches found")
	}
	match := matches[0]

	funcName := strings.TrimSpace(match[1])
	inputArgs := strings.TrimSpace(match[2])
	outputArgs := strings.TrimSpace(match[4])

	input, err := NewType("tuple(" + inputArgs + ")")
	if err != nil {
		return "", nil, nil, err
	}
	output, err := NewType("tuple(" + outputArgs + ")")
	if err != nil {
		return "", nil, nil, err
	}
	return funcName, input, output, nil
}

// Event is a triggered log mechanism
type Event struct {
	Name      string
	Anonymous bool
	Inputs    *Type
}

// Sig returns the signature of the event
func (e *Event) Sig() string {
	return buildSignature(e.Name, e.Inputs)
}

func (e *Event) DetailedSig() string {
	return buildHumanSignature(e.Name, e.Inputs)
}

// ID returns the id of the event used during logs
func (e *Event) ID() (res web3.Hash) {
	k := acquireKeccak()
	k.Write([]byte(e.Sig()))
	dst := k.Sum(nil)
	releaseKeccak(k)
	copy(res[:], dst)
	return
}

// MustNewEvent creates a new solidity event object or fails
func MustNewEvent(name string) *Event {
	evnt, err := NewEvent(name)
	if err != nil {
		panic(err)
	}
	return evnt
}

// NewEvent creates a new solidity event object using the signature
func NewEvent(name string) (*Event, error) {
	name, typ, err := parseEventSignature(name)
	if err != nil {
		return nil, err
	}
	return NewEventFromType(name, typ), nil
}

func parseEventSignature(name string) (string, *Type, error) {
	name = strings.TrimPrefix(name, "event ")
	if !strings.HasSuffix(name, ")") {
		return "", nil, fmt.Errorf("failed to parse input, expected 'name(types)'")
	}
	indx := strings.Index(name, "(")
	if indx == -1 {
		return "", nil, fmt.Errorf("failed to parse input, expected 'name(types)'")
	}

	funcName, signature := name[:indx], name[indx:]
	signature = "tuple" + signature

	typ, err := NewType(signature)
	if err != nil {
		return "", nil, err
	}
	return funcName, typ, nil
}

// NewEventFromType creates a new solidity event object using the name and type
func NewEventFromType(name string, typ *Type) *Event {
	return &Event{Name: name, Inputs: typ}
}

// Match checks wheter the log is from this event
func (e *Event) Match(log *web3.Log) bool {
	if len(log.Topics) == 0 {
		return false
	}
	if log.Topics[0] != e.ID() {
		return false
	}
	return true
}

// ParseLog parses a log with this event
func (e *Event) ParseLog(log *web3.Log) (map[string]interface{}, error) {
	if !e.Match(log) {
		return nil, fmt.Errorf("log does not match this event")
	}
	return e.Inputs.ParseLog(log)
}

func buildSignature(name string, typ *Type) string {
	types := make([]string, len(typ.tuple))
	for i, input := range typ.tuple {
		types[i] = input.Elem.raw
	}
	return fmt.Sprintf("%v(%v)", name, strings.Join(types, ","))
}

func buildHumanSignature(name string, typ *Type) string {
	types := make([]string, len(typ.tuple))
	for i, input := range typ.tuple {
		types[i] = input.Elem.raw + " " + input.Name
	}
	return fmt.Sprintf("%v(%v)", name, strings.Join(types, ", "))
}

type argument struct {
	Name    string
	Type    *Type
	Indexed bool
}

type arguments []*argument

func (a *arguments) Type() *Type {
	inputs := []*TupleElem{}
	for _, i := range *a {
		inputs = append(inputs, &TupleElem{
			Name:    i.Name,
			Elem:    i.Type,
			Indexed: i.Indexed,
		})
	}

	tt := &Type{
		kind:  KindTuple,
		raw:   "tuple",
		tuple: inputs,
	}
	return tt
}

func (a *argument) UnmarshalJSON(data []byte) error {
	var arg *ArgumentStr
	if err := json.Unmarshal(data, &arg); err != nil {
		return fmt.Errorf("argument json err: %v", err)
	}

	t, err := NewTypeFromArgument(arg)
	if err != nil {
		return err
	}

	a.Type = t
	a.Name = arg.Name
	a.Indexed = arg.Indexed
	return nil
}

// ArgumentStr encodes a type object
type ArgumentStr struct {
	Name       string
	Type       string
	Indexed    bool
	Components []*ArgumentStr
}

var keccakPool = sync.Pool{
	New: func() interface{} {
		return sha3.NewLegacyKeccak256()
	},
}

func acquireKeccak() hash.Hash {
	return keccakPool.Get().(hash.Hash)
}

func releaseKeccak(k hash.Hash) {
	k.Reset()
	keccakPool.Put(k)
}

func NewABIFromList(humanReadableAbi []string) (*ABI, error) {
	res := &ABI{}
	for _, c := range humanReadableAbi {
		if strings.HasPrefix(c, "function ") {
			method, err := NewMethod(c)
			if err != nil {
				return nil, err
			}
			res.addMethod(method)
		} else if strings.HasPrefix(c, "event ") {
			evnt, err := NewEvent(c)
			if err != nil {
				return nil, err
			}
			res.addEvent(evnt)
		} else {
			return nil, fmt.Errorf("either event or function expected")
		}
	}
	return res, nil
}
