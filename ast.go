package eval

import (
	"fmt"
	"github.com/apaxa-go/helper/goh/constanth"
	"github.com/apaxa-go/helper/goh/tokenh"
	"github.com/apaxa-go/helper/reflecth"
	"go/ast"
	"go/constant"
	"go/token"
	"reflect"
)

func (expr *Expression) astIdent(e *ast.Ident, args Args) (r Value, err *posError) {
	switch e.Name {
	case "true":
		return MakeDataUntypedConst(constant.MakeBool(true)), nil
	case "false":
		return MakeDataUntypedConst(constant.MakeBool(false)), nil
	case "nil":
		return MakeDataNil(), nil
	}

	switch {
	case isBuiltInFunc(e.Name):
		return MakeBuiltInFunc(e.Name), nil
	case isBuiltInType(e.Name):
		return MakeType(builtInTypes[e.Name]), nil
	default:
		var ok bool
		r, ok = args[e.Name]
		if !ok {
			err = identUndefinedError(e.Name).pos(e)
		}
		return
	}
}

// astSelectorExpr can:
// 	* get field from struct or pointer to struct
//	* get method (defined with receiver V) from variable of type V or pointer variable to type V
//	* get method (defined with pointer receiver V) from pointer variable to type V
func (expr *Expression) astSelectorExpr(e *ast.SelectorExpr, args Args) (r Value, err *posError) {
	// Calc object (left of '.')
	x, err := expr.astExpr(e.X, args)
	if err != nil {
		return
	}

	// Extract field/method name
	if e.Sel == nil {
		return nil, &posError{msg: string(*invAstSelectorError()), pos: e.Pos()} // Looks like unreachable if e generated by parsing source (not by hand). It is not possible to use intError.pos here because it cause panic.
	}
	name := e.Sel.Name

	switch x.Kind() {
	case Package:
		var ok bool
		r, ok = x.Package()[name]
		if !ok {
			return nil, identUndefinedError("." + name).pos(e)
		}

		return
	case Datas:
		xD := x.Data()
		if xD.Kind() != Regular {
			return nil, invSelectorXError(x).pos(e)
		}
		xV := xD.Regular()

		// If kind is pointer than try to get method.
		// If no method can be get than dereference pointer.
		if xV.Kind() == reflect.Ptr {
			if method := xV.MethodByName(name); method.IsValid() {
				return MakeDataRegular(method), nil
			}
			xV = xV.Elem()
		}

		// If kind is struct than try to get field
		if xV.Kind() == reflect.Struct {
			if field := fieldByName(xV, name, expr.pkgPath); field.IsValid() {
				return MakeDataRegular(field), nil
			}
		}

		// Last case - try to get method (on already dereferenced variable)
		if method := xV.MethodByName(name); method.IsValid() {
			return MakeDataRegular(method), nil
		}

		return nil, identUndefinedError("." + name).pos(e)
	case Type:
		xT := x.Type()
		if xT.Kind() == reflect.Interface {
			return nil, interfaceMethodExpr().pos(e) // BUG
		}

		f, ok := xT.MethodByName(name)
		if !ok || !f.Func.IsValid() {
			return nil, selectorUndefIdentError(xT, name).pos(e)
		}
		return MakeDataRegular(f.Func), nil
	default:
		return nil, invSelectorXError(x).pos(e)
	}
}

func (expr *Expression) astBinaryExpr(e *ast.BinaryExpr, args Args) (r Value, err *posError) {
	x, err := expr.astExprAsData(e.X, args)
	if err != nil {
		return
	}
	y, err := expr.astExprAsData(e.Y, args)
	if err != nil {
		return
	}

	// Perform calc depending on operation type
	switch {
	case tokenh.IsComparison(e.Op):
		return upT(compareOp(x, e.Op, y)).pos(e)
	case tokenh.IsShift(e.Op):
		return upT(shiftOp(x, e.Op, y)).pos(e)
	default:
		return upT(binaryOp(x, e.Op, y)).pos(e)
	}
}

func (expr *Expression) astBasicLit(e *ast.BasicLit, args Args) (r Value, err *posError) {
	rC := constant.MakeFromLiteral(e.Value, e.Kind, 0)
	if rC.Kind() == constant.Unknown {
		return nil, syntaxInvBasLitError(e.Value).pos(e) // looks like unreachable if e generated by parsing source (not by hand).
	}
	return MakeDataUntypedConst(rC), nil
}

func (expr *Expression) astParenExpr(e *ast.ParenExpr, args Args) (r Value, err *posError) {
	return expr.astExpr(e.X, args)
}

func (expr *Expression) astCallExpr(e *ast.CallExpr, args Args) (r Value, err *posError) {
	// Resolve func
	f, err := expr.astExpr(e.Fun, args)
	if err != nil {
		return
	}

	// Resolve args
	var eArgs []Data
	if f.Kind() != BuiltInFunc { // for built-in funcs required []Value, not []Data
		eArgs = make([]Data, len(e.Args))
		for i := range e.Args {
			eArgs[i], err = expr.astExprAsData(e.Args[i], args)
			if err != nil {
				return
			}
		}
	}

	var intErr *intError
	switch f.Kind() {
	case Datas:
		fD := f.Data()
		switch fD.Kind() {
		case Regular:
			r, intErr = callRegular(fD.Regular(), eArgs, e.Ellipsis != token.NoPos)
		default:
			intErr = callNonFuncError(f)
		}
	case BuiltInFunc:
		eArgs := make([]Value, len(e.Args))
		for i := range e.Args {
			eArgs[i], err = expr.astExpr(e.Args[i], args)
			if err != nil {
				return
			}
		}
		r, intErr = callBuiltInFunc(f.BuiltInFunc(), eArgs, e.Ellipsis != token.NoPos)
	case Type:
		if e.Ellipsis != token.NoPos {
			return nil, convertWithEllipsisError(f.Type()).pos(e)
		}
		r, intErr = convertCall(f.Type(), eArgs)
	default:
		intErr = callNonFuncError(f)
	}

	err = intErr.pos(e)
	return
}

func (expr *Expression) astStarExpr(e *ast.StarExpr, args Args) (r Value, err *posError) {
	v, err := expr.astExpr(e.X, args)
	if err != nil {
		return
	}

	switch {
	case v.Kind() == Type:
		return MakeType(reflect.PtrTo(v.Type())), nil
	case v.Kind() == Datas && v.Data().Kind() == Regular && v.Data().Regular().Kind() == reflect.Ptr:
		return MakeDataRegular(v.Data().Regular().Elem()), nil
	default:
		return nil, indirectInvalError(v).pos(e)
	}
}

func (expr *Expression) astUnaryExpr(e *ast.UnaryExpr, args Args) (r Value, err *posError) {
	x, err := expr.astExprAsData(e.X, args)
	if err != nil {
		return
	}
	return upT(unaryOp(e.Op, x)).pos(e)
}

func (expr *Expression) astChanType(e *ast.ChanType, args Args) (r Value, err *posError) {
	t, err := expr.astExprAsType(e.Value, args)
	if err != nil {
		return
	}
	return MakeType(reflect.ChanOf(reflecth.ChanDirFromAst(e.Dir), t)), nil
}

// Here implements only for list of arguments types ("func(a ...string)").
// For ellipsis array literal ("[...]int{1,2}") see astCompositeLit.
// For ellipsis argument for call ("f(1,a...)") see astCallExpr.
func (expr *Expression) astEllipsis(e *ast.Ellipsis, args Args) (r Value, err *posError) {
	t, err := expr.astExprAsType(e.Elt, args)
	if err != nil {
		return
	}
	return MakeType(reflect.SliceOf(t)), nil
}

func (expr *Expression) astFuncType(e *ast.FuncType, args Args) (r Value, err *posError) {
	in, variadic, err := expr.funcTranslateArgs(e.Params, true, args)
	if err != nil {
		return
	}
	out, _, err := expr.funcTranslateArgs(e.Results, false, args)
	if err != nil {
		return
	}
	return MakeType(reflect.FuncOf(in, out, variadic)), nil
}

func (expr *Expression) astArrayType(e *ast.ArrayType, args Args) (r Value, err *posError) {
	t, err := expr.astExprAsType(e.Elt, args)
	if err != nil {
		return
	}

	switch e.Len {
	case nil: // Slice
		rT := reflect.SliceOf(t)
		return MakeType(rT), nil
	default: // Array
		// eval length
		var l Data
		l, err = expr.astExprAsData(e.Len, args) // Case with ellipsis in length must be caught by caller (astCompositeLit)
		if err != nil {
			return
		}

		// convert length to int
		var lInt int
		switch l.Kind() {
		case TypedConst:
			var ok bool
			lInt, ok = constanth.IntVal(l.TypedConst().Untyped())
			if !l.TypedConst().AssignableTo(reflecth.TypeInt()) || !ok { // AssignableTo should be enough
				return nil, arrayBoundInvBoundError(l).pos(e.Len)
			}
		case UntypedConst:
			var ok bool
			lInt, ok = constanth.IntVal(l.UntypedConst())
			if !ok {
				return nil, arrayBoundInvBoundError(l).pos(e.Len)
			}
		default:
			return nil, arrayBoundInvBoundError(l).pos(e.Len)
		}

		// validate length
		if lInt < 0 {
			return nil, arrayBoundNegError().pos(e)
		}

		// make array
		rT := reflect.ArrayOf(lInt, t)
		return MakeType(rT), nil
	}
}

func (expr *Expression) astIndexExpr(e *ast.IndexExpr, args Args) (r Value, err *posError) {
	x, err := expr.astExprAsData(e.X, args)
	if err != nil {
		return
	}

	i, err := expr.astExprAsData(e.Index, args)
	if err != nil {
		return nil, err
	}

	var intErr *intError
	switch x.Kind() {
	case Regular:
		switch x.Regular().Kind() {
		case reflect.Map:
			r, intErr = indexMap(x.Regular(), i)
		default:
			r, intErr = indexOther(x.Regular(), i)
		}
	case TypedConst:
		r, intErr = indexConstant(x.TypedConst().Untyped(), i)
	case UntypedConst:
		r, intErr = indexConstant(x.UntypedConst(), i)
	default:
		intErr = invIndexOpError(x, i)
	}

	err = intErr.pos(e)
	return
}

func (expr *Expression) astSliceExpr(e *ast.SliceExpr, args Args) (r Value, err *posError) {
	x, err := expr.astExprAsData(e.X, args)
	if err != nil {
		return
	}

	indexResolve := func(e ast.Expr) (iInt int, err1 *posError) {
		var i Data
		if e != nil {
			i, err1 = expr.astExprAsData(e, args)
			if err1 != nil {
				return
			}
		}

		var intErr *intError
		iInt, intErr = getSliceIndex(i)
		err1 = intErr.pos(e)
		return
	}

	// Calc indexes
	low, err := indexResolve(e.Low)
	if err != nil {
		return
	}
	high, err := indexResolve(e.High)
	if err != nil {
		return
	}
	var max int
	if e.Slice3 {
		max, err = indexResolve(e.Max)
		if err != nil {
			return
		}
	}

	var v reflect.Value
	switch x.Kind() {
	case Regular:
		v = x.Regular()
	case TypedConst:
		// Typed constant in slice expression may be only of string kind
		if x.TypedConst().Type().Kind() != reflect.String {
			return nil, sliceInvTypeError(x).pos(e.X)
		}
		v = x.TypedConst().Value()

		//xStr, ok := constanth.StringVal(x.TypedConst().Untyped())
		//if !ok {
		//	return nil, sliceInvTypeError(x).pos(e.X)
		//}
		//v = reflect.ValueOf(xStr)
	case UntypedConst:
		// Untyped constant in slice expression may be only of string kind
		xStr, ok := constanth.StringVal(x.UntypedConst())
		if !ok {
			return nil, sliceInvTypeError(x).pos(e.X)
		}
		v = reflect.ValueOf(xStr)
	default:
		return nil, sliceInvTypeError(x).pos(e.X)
	}

	var intErr *intError
	if e.Slice3 {
		r, intErr = slice3(v, low, high, max)
	} else {
		r, intErr = slice2(v, low, high)
	}

	err = intErr.pos(e)
	return
}

func (expr *Expression) astCompositeLit(e *ast.CompositeLit, args Args) (r Value, err *posError) {
	// type
	var vT reflect.Type
	// case where type is an ellipsis array
	if aType, ok := e.Type.(*ast.ArrayType); ok {
		if _, ok := aType.Len.(*ast.Ellipsis); ok {
			// Resolve array elements type
			vT, err = expr.astExprAsType(aType.Elt, args)
			if err != nil {
				return
			}
			vT = reflect.ArrayOf(len(e.Elts), vT)
		}
	}
	// other cases
	if vT == nil {
		vT, err = expr.astExprAsType(e.Type, args)
		if err != nil {
			return
		}
	}

	// Construct
	var intErr *intError
	switch vT.Kind() {
	case reflect.Struct:
		var withKeys bool
		if len(e.Elts) == 0 {
			withKeys = true // Treat empty initialization list as with keys
		} else {
			_, withKeys = e.Elts[0].(*ast.KeyValueExpr)
		}

		switch withKeys {
		case true:
			elts := make(map[string]Data)
			for i := range e.Elts {
				kve, ok := e.Elts[i].(*ast.KeyValueExpr)
				if !ok {
					return nil, initMixError().pos(e)
				}

				key, ok := kve.Key.(*ast.Ident)
				if !ok {
					return nil, initStructInvFieldNameError().pos(kve)
				}

				elts[key.Name], err = expr.astExprAsData(kve.Value, args)
				if err != nil {
					return
				}
			}
			r, intErr = compositeLitStructKeys(vT, elts, expr.pkgPath)
		case false:
			elts := make([]Data, len(e.Elts))
			for i := range e.Elts {
				if _, ok := e.Elts[i].(*ast.KeyValueExpr); ok {
					return nil, initMixError().pos(e)
				}

				elts[i], err = expr.astExprAsData(e.Elts[i], args)
				if err != nil {
					return
				}
			}
			r, intErr = compositeLitStructOrdered(vT, elts, expr.pkgPath)
		}
	case reflect.Array, reflect.Slice:
		elts := make(map[int]Data)
		nextIndex := 0
		for i := range e.Elts {
			var valueExpr ast.Expr
			if kve, ok := e.Elts[i].(*ast.KeyValueExpr); ok {
				var v Data
				v, err = expr.astExprAsData(kve.Key, args)
				if err != nil {
					return
				}
				switch v.Kind() {
				case TypedConst:
					if v.TypedConst().Type() != reflecth.TypeInt() {
						return nil, initArrayInvIndexError().pos(kve)
					}
					var ok bool
					nextIndex, ok = constanth.IntVal(v.TypedConst().Untyped())
					if !ok || nextIndex < 0 {
						return nil, initArrayInvIndexError().pos(kve)
					}
				case UntypedConst:
					var ok bool
					nextIndex, ok = constanth.IntVal(v.UntypedConst())
					if !ok || nextIndex < 0 {
						return nil, initArrayInvIndexError().pos(kve)
					}
				default:
					return nil, initArrayInvIndexError().pos(kve)
				}

				valueExpr = kve.Value
			} else {
				valueExpr = e.Elts[i]
			}

			if _, ok := elts[nextIndex]; ok {
				return nil, initArrayDupIndexError(nextIndex).pos(e.Elts[i])
			}

			elts[nextIndex], err = expr.astExprAsData(valueExpr, args)
			if err != nil {
				return
			}
			nextIndex++
		}

		r, intErr = compositeLitArrayLike(vT, elts)
	case reflect.Map:
		elts := make(map[Data]Data)
		for i := range e.Elts {
			kve, ok := e.Elts[i].(*ast.KeyValueExpr)
			if !ok {
				return nil, initMapMisKeyError().pos(e.Elts[i])
			}

			var key Data
			key, err = expr.astExprAsData(kve.Key, args)
			if err != nil {
				return
			}
			elts[key], err = expr.astExprAsData(kve.Value, args) // looks like it is impossible to overwrite value here because key!=prev_key (it is interface)
			if err != nil {
				return
			}
		}

		r, intErr = compositeLitMap(vT, elts)
	default:
		return nil, initInvTypeError(vT).pos(e.Type)
	}

	err = intErr.pos(e)
	return
}

func (expr *Expression) astTypeAssertExpr(e *ast.TypeAssertExpr, args Args) (r Value, err *posError) {
	x, err := expr.astExprAsData(e.X, args)
	if err != nil {
		return
	}
	if x.Kind() != Regular || x.Regular().Kind() != reflect.Interface {
		return nil, typeAssertLeftInvalError(x).pos(e)
	}
	t, err := expr.astExprAsType(e.Type, args)
	if err != nil {
		return
	}
	rV, ok, valid := reflecth.TypeAssert(x.Regular(), t)
	if !valid {
		return nil, typeAssertImposError(x.Regular(), t).pos(e)
	}
	if !ok {
		return nil, typeAssertFalseError(x.Regular(), t).pos(e)
	}
	return MakeDataRegular(rV), nil
}

func (expr *Expression) astMapType(e *ast.MapType, args Args) (r Value, err *posError) {
	k, err := expr.astExprAsType(e.Key, args)
	if err != nil {
		return
	}
	v, err := expr.astExprAsType(e.Value, args)
	if err != nil {
		return
	}

	defer func() {
		if rec := recover(); rec != nil {
			err = newIntError(fmt.Sprint(rec)).pos(e)
		}
	}()
	rT := reflect.MapOf(k, v)

	r = MakeType(rT)
	return
}

// BUG(a.bekker): Eval* currently does not generate wrapper methods for embedded fields in structures (see reflect.StructOf for more details).

func (expr *Expression) astStructType(e *ast.StructType, args Args) (r Value, err *posError) {
	// Looks like e.Incomplete does not mean anything in our case.
	if e.Fields == nil {
		return nil, &posError{msg: string(*invAstNilStructFieldsError()), pos: e.Pos()} // Looks like unreachable if e generated by parsing source (not by hand). It is not possible to use intError.pos here because it cause panic.
	}

	extractTag := func(l *ast.BasicLit) (tag reflect.StructTag, err *posError) {
		if l == nil {
			return
		}
		tagC := constant.MakeFromLiteral(l.Value, l.Kind, 0)
		if tagC.Kind() != constant.String {
			err = invAstNonStringTagError().pos(l)
			return
		}
		tag = reflect.StructTag(constant.StringVal(tagC))
		return
	}

	fields := make([]reflect.StructField, e.Fields.NumFields())
	i := 0
	for _, fs := range e.Fields.List {
		// Determine common fields type
		var fsT reflect.Type
		fsT, err = expr.astExprAsType(fs.Type, args)
		if err != nil {
			return
		}

		switch fs.Names {
		case nil: // Anonymous field
			fields[i].Name = ""
			fields[i].PkgPath = expr.pkgPath
			fields[i].Type = fsT
			fields[i].Anonymous = true
			fields[i].Tag, err = extractTag(fs.Tag)
			if err != nil {
				return
			}
			i++
		default: // Named fields
			for _, f := range fs.Names {
				fields[i].Name = f.Name
				fields[i].PkgPath = expr.pkgPath
				fields[i].Type = fsT
				fields[i].Anonymous = false
				fields[i].Tag, err = extractTag(fs.Tag)
				if err != nil {
					return
				}
				i++
			}
		}
	}

	defer func() {
		if rec := recover(); rec != nil {
			err = newIntError(fmt.Sprint(rec)).pos(e)
		}
	}()
	r = MakeType(reflect.StructOf(fields))
	return
}

// BUG(a.bekker): Only empty interface type can be declared in expression.

func (expr *Expression) astInterfaceType(e *ast.InterfaceType, args Args) (r Value, err *posError) {
	if e.Methods == nil {
		return nil, &posError{msg: string(*invAstNilInterfaceMethodsError()), pos: e.Pos()} // Looks like unreachable if e generated by parsing source (not by hand). It is not possible to use intError.pos here because it cause panic.
	}
	if len(e.Methods.List) != 0 {
		return nil, unsupportedInterfaceTypeError().pos(e)
	}
	r = MakeType(reflecth.TypeEmptyInterface())
	return
}

func (expr *Expression) astExprAsData(e ast.Expr, args Args) (r Data, err *posError) {
	var rValue Value
	rValue, err = expr.astExpr(e, args)
	if err != nil {
		return
	}

	switch rValue.Kind() {
	case Datas:
		r = rValue.Data()
	default:
		err = notExprError(rValue).pos(e)
	}
	return
}

func (expr *Expression) astExprAsType(e ast.Expr, args Args) (r reflect.Type, err *posError) {
	var rValue Value
	rValue, err = expr.astExpr(e, args)
	if err != nil {
		return
	}

	switch rValue.Kind() {
	case Type:
		r = rValue.Type()
	default:
		err = notTypeError(rValue).pos(e)
	}
	return
}

func (expr *Expression) astExpr(e ast.Expr, args Args) (r Value, err *posError) {
	if e == nil {
		return nil, invAstNilError().noPos()
	}

	switch v := e.(type) {
	case *ast.Ident:
		return expr.astIdent(v, args)
	case *ast.SelectorExpr:
		return expr.astSelectorExpr(v, args)
	case *ast.BinaryExpr:
		return expr.astBinaryExpr(v, args)
	case *ast.BasicLit:
		return expr.astBasicLit(v, args)
	case *ast.ParenExpr:
		return expr.astParenExpr(v, args)
	case *ast.CallExpr:
		return expr.astCallExpr(v, args)
	case *ast.StarExpr:
		return expr.astStarExpr(v, args)
	case *ast.UnaryExpr:
		return expr.astUnaryExpr(v, args)
	case *ast.Ellipsis:
		return expr.astEllipsis(v, args)
	case *ast.ChanType:
		return expr.astChanType(v, args)
	case *ast.FuncType:
		return expr.astFuncType(v, args)
	case *ast.ArrayType:
		return expr.astArrayType(v, args)
	case *ast.IndexExpr:
		return expr.astIndexExpr(v, args)
	case *ast.SliceExpr:
		return expr.astSliceExpr(v, args)
	case *ast.CompositeLit:
		return expr.astCompositeLit(v, args)
	case *ast.TypeAssertExpr:
		return expr.astTypeAssertExpr(v, args)
	case *ast.MapType:
		return expr.astMapType(v, args)
	case *ast.StructType:
		return expr.astStructType(v, args)
	case *ast.InterfaceType:
		return expr.astInterfaceType(v, args)
	default:
		// BadExpr - no need to implement
		// FuncLit - do not want to implement (too hard, this project only evaluate expression)
		// KeyValueExpr - implemented in-place, not here
		return nil, invAstUnsupportedError(e).pos(e)
	}
}
