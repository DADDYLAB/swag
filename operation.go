package swag

import (
	"fmt"
	"go/ast"
	goparser "go/parser"
	"go/token"
	"log"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"golang.org/x/tools/go/packages"

	"github.com/go-openapi/jsonreference"
	"github.com/go-openapi/spec"
	"github.com/pkg/errors"
)

// Operation describes a single API operation on a path.
// For more information: https://github.com/swaggo/swag#api-operation
type Operation struct {
	HTTPMethod string
	Path       string
	spec.Operation

	parser *Parser

	// Struct 结构
	SP *StructPto

	LastPto Pto
}

type Pto interface {
	Add(pto *FieldPto)
	AddParent(pto *FieldPto)
	GetLevel() int
	GetParent() Pto
	ShouldAutoPkgFunc() bool
}

type StructPto struct {
	Name     string
	Required bool
	Desc     string
	IsArray  bool
	Fields   []*FieldPto

	FullName      string
	ShouldAutoPkg bool
}

func (self *StructPto) FindSPOrChildSPBy(name string) Pto {
	if name == self.Name {
		return self
	}

	for i, e := range self.Fields {
		if e.Name == name {
			return self.Fields[i]
		}
		for _, f := range e.Child {
			return f.FindSPOrChildSPBy(name)
		}
	}
	return nil
}

func (self *FieldPto) FindSPOrChildSPBy(name string) Pto {
	if name == self.Name {
		return self
	}

	for i, e := range self.Child {
		if e.Name == name {
			return self.Child[i]
		}

		for _, f := range e.Child {
			return f.FindSPOrChildSPBy(name)
		}
	}
	return nil
}

func (self *StructPto) ShouldAutoPkgFunc() bool {
	return self.ShouldAutoPkg
}

func (self *StructPto) Add(pto *FieldPto) {
	if self.Fields == nil {
		self.Fields = make([]*FieldPto, 0)
	}
	self.Fields = append(self.Fields, pto)
}

func (self *StructPto) AddParent(pto *FieldPto) {
	if self.Fields == nil {
		self.Fields = make([]*FieldPto, 0)
	}
	self.Fields = append(self.Fields, pto)
}

func (self *StructPto) GetParent() Pto {
	return self
}

const reTemplate = `
	type {{.StructName}} struct{
		{{range $index, $field := .Fields}}
			{{$field.Name}} {{$field.Type}}  {{if $field.Ex }} ^{{$field.Ex}}^ {{end}} // {{$field.Desc}}
		{{end}}
	}
`

func (self *StructPto) Map2GoFile(structPrefix string, srcAstFile *ast.File, operation *Operation) string {
	Println("start map file ... ")
	var result strings.Builder
	result.WriteString("package swagauto")
	result.WriteByte('\n')

	parsed, err := template.New("level").Parse(reTemplate)
	if err != nil {
		return ""
	}

	type Temp struct {
		StructName string
		Fields     []FieldPto
	}

	var newFields []FieldPto
	var allFieldDecls []string

	if strings.HasSuffix(self.Name, "_") {
		// 末尾有_
		self.Name = self.Name[:len(self.Name)-1]
		self.ShouldAutoPkg = true
	}
	self.Name = structPrefix + strings.ReplaceAll(strings.Title(self.Name), "-", "")

	for _, e := range self.Fields {
		e.Name = strings.Title(strings.ReplaceAll(e.Name, "-", ""))

		if e.Type == "struct" {
			result := e.Map2GoStruct(operation, srcAstFile)
			allFieldDecls = append(allFieldDecls, result)

			e.Type = e.Name
		}

		if !IsGolangPrimitiveType(e.Type) {
			ref := strings.Split(e.Type, ".")
			if len(ref) == 2 {
				err = operation.registerSchemaType(e.Type, srcAstFile)
				if err != nil {
					Println(err, " -- err -- ")
					return ""
				}
			}
		}

		if e.IsArray {
			e.Type = "[]" + e.Type
		}

		if e.Required {
			e.Ex += " validate(required) "
		}

		newFields = append(newFields, *e)
	}

	err = parsed.Execute(&result, &Temp{
		StructName: self.Name,
		Fields:     newFields,
	})
	if err != nil {
		log.Fatalf("execution failed: %s", err)
	}

	for _, e := range allFieldDecls {
		result.WriteString(" \n ")
		result.WriteString(e)
	}

	var r string
	r = strings.ReplaceAll(result.String(), "(", ":\"")
	r = strings.ReplaceAll(r, ")", "\"")
	r = strings.ReplaceAll(r, "^", "`")
	//fmt.Println(r)
	return r
}

func (self *StructPto) GetLevel() int {
	return 0
}

type FieldPto struct {
	Name     string
	Required bool
	Level    int
	Type     string
	Desc     string
	Ex       string
	Parent   Pto

	IsArray bool
	Child   []*FieldPto

	ShouldAutoPkg bool
}

func (self *FieldPto) ShouldAutoPkgFunc() bool {
	return self.ShouldAutoPkg
}

func (self *FieldPto) Add(pto *FieldPto) {
	if self.Child == nil {
		self.Child = make([]*FieldPto, 0)
	}
	self.Child = append(self.Child, pto)
}

func (self *FieldPto) AddParent(pto *FieldPto) {
	if self.Parent != nil {
		self.Parent.Add(pto)
	}
}

func (self *FieldPto) GetLevel() int {
	return self.Level
}

func (self *FieldPto) GetParent() Pto {
	return self.Parent
}

func (self *FieldPto) Map2GoStruct(operation *Operation, srcAstFile *ast.File) string {
	// 这个Field必须是Struct
	var result strings.Builder

	parsed, err := template.New("level").Parse(reTemplate)
	if err != nil {
		return ""
	}

	type Temp struct {
		StructName string
		Fields     []FieldPto
	}

	var newFields []FieldPto
	var allFieldDecls []string

	if self.Type == "struct" {
		if strings.HasSuffix(self.Name, "_") {
			// 末尾有_
			self.Name = self.Name[:len(self.Name)-1]
			self.ShouldAutoPkg = true
		}
		self.Name = strings.ReplaceAll(strings.Title(self.Name), "-", "")
		for _, e := range self.Child {
			e.Name = strings.Title(strings.ReplaceAll(e.Name, "-", ""))

			if e.Type == "struct" {
				result := e.Map2GoStruct(operation, srcAstFile)
				allFieldDecls = append(allFieldDecls, result)
				e.Type = e.Name
			}

			if !IsGolangPrimitiveType(e.Type) {
				ref := strings.Split(e.Type, ".")
				if len(ref) == 2 {
					err = operation.registerSchemaType(e.Type, srcAstFile)
					if err != nil {
						Println(err, " -- err -- ")
						return ""
					}
				}
			}

			if e.IsArray {
				e.Type = "[]" + e.Type
			}

			if e.Required {
				e.Ex += " validate(required) "
			}

			newFields = append(newFields, *e)
		}
	}

	err = parsed.Execute(&result, &Temp{
		StructName: self.Name,
		Fields:     newFields,
	})
	if err != nil {
		log.Fatalf("execution failed: %s", err)
	}
	for _, e := range allFieldDecls {
		result.WriteString(" \n ")
		result.WriteString(e)
	}
	return strings.ReplaceAll(result.String(), "^", "`")
}

var mimeTypeAliases = map[string]string{
	"json":                  "application/json",
	"xml":                   "text/xml",
	"plain":                 "text/plain",
	"html":                  "text/html",
	"mpfd":                  "multipart/form-data",
	"form":                  "multipart/form-data",
	"x-www-form-urlencoded": "application/x-www-form-urlencoded",
	"x-form":                "application/x-www-form-urlencoded",
	"json-api":              "application/vnd.api+json",
	"json-stream":           "application/x-json-stream",
	"octet-stream":          "application/octet-stream",
	"png":                   "image/png",
	"jpeg":                  "image/jpeg",
	"gif":                   "image/gif",
}

var mimeTypePattern = regexp.MustCompile("^[^/]+/[^/]+$")

// NewOperation creates a new Operation with default properties.
// map[int]Response
func NewOperation() *Operation {
	return &Operation{
		HTTPMethod: "get",
		Operation: spec.Operation{
			OperationProps: spec.OperationProps{},
		},
	}
}

// ParseComment parses comment for given comment string and returns error if error occurs.
func (operation *Operation) ParseComment(comment string, astFile *ast.File) error {
	commentLine := strings.TrimSpace(strings.TrimLeft(comment, "//"))
	if len(commentLine) == 0 {
		return nil
	}

	attribute := strings.Fields(commentLine)[0]
	lineRemainder := strings.TrimSpace(commentLine[len(attribute):])
	switch strings.ToLower(attribute) {
	case "@desc":
		if operation.Description == "" {
			operation.Description = lineRemainder
		} else {
			operation.Description += "\n" + lineRemainder
		}
	case "@title":
		operation.Summary = lineRemainder
	case "@id":
		operation.ID = lineRemainder
	case "@tags":
		operation.ParseTagsComment(lineRemainder)
	case "@accept":
		if err := operation.ParseAcceptComment(lineRemainder); err != nil {
			return err
		}
	case "@produce":
		if err := operation.ParseProduceComment(lineRemainder); err != nil {
			return err
		}
	case "@param":
		if err := operation.ParseParamComment(lineRemainder, astFile); err != nil {
			return err
		}
	case "@success":
		if err := operation.ParseResponseComment(lineRemainder, astFile); err != nil {
			return err
		}
	case "@fail":
		if err := operation.ParseFailComment(lineRemainder, astFile); err != nil {
			return err
		}
	case "@single":
		if err := operation.ParseSingleComment(lineRemainder); err != nil {
			return err
		}
	case "@header":
		if err := operation.ParseResponseHeaderComment(lineRemainder, astFile); err != nil {
			return err
		}
	case "@router":
		if err := operation.ParseRouterComment(lineRemainder); err != nil {
			return err
		}
	case "@security":
		if err := operation.ParseSecurityComment(lineRemainder); err != nil {
			return err
		}
	case "@deprecated":
		operation.Deprecate()
	}
	return nil
}

var paramPattern = regexp.MustCompile(`(\S+)[\s]+([\w]+)[\s]+([\S.]+)[\s]+([\w]+)[\s]+"([^"]+)"`)

func (operation *Operation) findMatches(matches []string) (name string, schemaType string, paramType string, require bool, desc string) {
	var inT = []string{"query", "path", "header", "body", "form"}
	var defaultParam string
	if operation.HTTPMethod == "GET" {
		defaultParam = "query"
	} else if operation.HTTPMethod == "PUT" || operation.HTTPMethod == "DELETE" || operation.HTTPMethod == "PATCH" {
		defaultParam = "path"
	} else if operation.HTTPMethod == "POST" {
		defaultParam = "body"
	}

	if len(matches) < 3 {
		if len(matches) == 1 {
			if matches[0] == "-" {
				return "EndStruct", "", "", false, ""
			}
		}
		return "", "", "", false, ""
	}

	name = matches[0]
	schemaType = matches[1]

	switch len(matches) {
	case 3:
		// @Param   --name       string   筛选姓名
		paramType = defaultParam
		require = true
		desc = matches[2]
		return
	case 4:
		// @Param   --name       string    T   筛选姓名
		// @Param   --name       string    body   筛选姓名
		// @Param   --name       string    default()   筛选姓名
		desc = matches[len(matches)-1]

		if has(inT, matches[2]) {
			paramType = matches[2]
		} else if matches[2] == "true" || matches[2] == "false" ||
			matches[2] == "T" || matches[2] == "F" {
			paramType = defaultParam
			require, _ = strconv.ParseBool(matches[2])
		} else {
			paramType = defaultParam
			require = true
		}
	case 5:
		// @Param   --name       string    body   T   筛选姓名
		// @Param   --name       string    body   default()   筛选姓名
		// @Param   --name       string    T     default()    筛选姓名
		desc = matches[len(matches)-1]

		if has(inT, matches[2]) {
			paramType = matches[2]
		} else if matches[2] == "true" || matches[2] == "false" ||
			matches[2] == "T" || matches[2] == "F" {
			paramType = defaultParam
			require, _ = strconv.ParseBool(matches[2])
		}

		if matches[3] == "true" || matches[3] == "false" ||
			matches[3] == "T" || matches[3] == "F" {
			require, _ = strconv.ParseBool(matches[3])
		} else {
			require = true
		}
	case 6:
		// @Param   --name       string    body   T  default()   筛选姓名
		desc = matches[len(matches)-1]

		paramType = matches[2]
		require, _ = strconv.ParseBool(matches[3])
	}
	if paramType == "form" {
		paramType = "formData"
	}
	return
}

func has(s []string, key string) bool {
	for i := range s {
		if key == s[i] {
			return true
		}
	}
	return false
}

// ParseParamComment parses params return []string of param properties
// E.g. @Param	queryText		formData	    string	           true		     "The email for login"   enums(1,2,3)
//              [param name]    [paramType]   [data type]     [is mandatory?]    [Comment]               [attribute(optional)]
// Also: @Param   some_id          int       (default:query)   (default:true)    "Some ID"
func (operation *Operation) ParseParamComment(commentLine string, astFile *ast.File) error {
	// Println(" start parse ... ", commentLine)

	name, schemaType, paramType, required, description := operation.findMatches(regexp.MustCompile(`\s{3,}`).Split(commentLine, -1))
	if len(name) == 0 {
		return errors.New(" Parse Error : Check you param len in : " + commentLine)
	}
	var param spec.Parameter
	// Println(" Find : ", name, schemaType, paramType, required, description)

	if strings.HasPrefix(name, "-") || schemaType == "struct" || schemaType == "array_struct" {
		return operation.ParseStruct(schemaType, name, required, description, commentLine)
	} else {
		// +++结束+++
		if operation.SP != nil {
			structPrefix := "Param"
			if err := operation.EndTheStruct(structPrefix, astFile); err != nil {
				return err
			}

			fullName := operation.SP.FullName
			if operation.SP.IsArray {
				fullName = "array_" + fullName
			}
			paSp := createParameter("body", operation.SP.Desc, operation.SP.Name, fullName, operation.SP.Required)
			operation.Operation.Parameters = append(operation.Operation.Parameters, paSp)
			operation.SP = nil
			operation.LastPto = nil
		}

		if name == "EndStruct" {
			return nil
		}

		// 普通参数
		switch paramType {
		case "query", "path", "header", "form", "formData", "body":
			if paramType == "body" {
				if strings.Contains(schemaType, ".") {
					// 引用注册
					sName := strings.Split(schemaType, ".")
					if len(sName) != 2 {
						panic(errors.New("see" + schemaType))
					}
					//Println(" == register == ", schemaType)

					if err := operation.registerSchemaType(DelArray(schemaType), astFile); err != nil {
						return err
					}
				}
			}
			param = createParameter(paramType, description, name, TransToValidSchemeType(schemaType), required)
		default:
			return fmt.Errorf("%s is not supported paramType", paramType)
		}
	}

	if err := operation.parseAndExtractionParamAttribute(commentLine, schemaType, &param); err != nil {
		return err
	}
	operation.Operation.Parameters = append(operation.Operation.Parameters, param)
	return nil
}

func (operation *Operation) registerSchemaType(schemaType string, astFile *ast.File) error {
	refSplit := strings.Split(schemaType, ".")
	if len(refSplit) != 2 {
		return nil
	}
	pkgName := refSplit[0]
	typeName := refSplit[1]

	if typeSpec, ok := operation.parser.TypeDefinitions[pkgName][typeName]; ok {
		operation.parser.registerTypes[schemaType] = typeSpec
		return nil
	}

	typeSpecMap := make(map[string]*ast.TypeSpec)
	if astFile == nil {
		return fmt.Errorf("can not register schema type: %q reason: astFile == nil", schemaType)
	}

	for _, imp := range astFile.Imports {
		impPath := strings.Replace(imp.Path.Value, `"`, ``, -1)

		if strings.HasSuffix(impPath, "/"+pkgName) || (imp.Name != nil && imp.Name.Name == pkgName) {
			var err error

			conf := &packages.Config{
				Mode: packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
			}
			ps, err := packages.Load(conf, impPath)
			if err != nil {
				return err
			}

			var nestFieldNames []string
			for i := range ps {
				for j := range ps[i].Syntax {
					for _, astDeclaration := range ps[i].Syntax[j].Decls {
						if generalDeclaration, ok := astDeclaration.(*ast.GenDecl); ok && generalDeclaration.Tok == token.TYPE {
							for _, astSpec := range generalDeclaration.Specs {
								if typeSpec, ok := astSpec.(*ast.TypeSpec); ok && typeSpec.Name.String() == typeName {
									if _, ok := typeSpecMap[pkgName+"."+typeName]; !ok {
										typeSpecMap[pkgName+"."+typeName] = typeSpec

										// 内部的结构体也记录
										if t, ok := typeSpec.Type.(*ast.StructType); ok {
											for _, field := range t.Fields.List {
												switch t := field.Type.(type) {
												case *ast.Ident:
													nestFieldNames = append(nestFieldNames, t.Name)
												case *ast.ArrayType:
													nestFieldNames = append(nestFieldNames, TransToValidSchemeType(fmt.Sprintf("%s", t.Elt)))
												default:
													continue
												}
											}
										}
									}
								}
								break
							}
						}
					}
				}
			}
			if len(nestFieldNames) > 0 {
				regNestFields(ps, nestFieldNames, pkgName, typeSpecMap)
			}
		}
	}

	if len(typeSpecMap) == 0 {
		return fmt.Errorf("can not find schema type: %q", schemaType)
	}

	if _, ok := operation.parser.TypeDefinitions[pkgName]; !ok {
		operation.parser.TypeDefinitions[pkgName] = make(map[string]*ast.TypeSpec)
	}

	for k, typeSpec := range typeSpecMap {
		refSplit := strings.Split(k, ".")
		pN := refSplit[0]
		pT := refSplit[1]

		operation.parser.TypeDefinitions[pN][pT] = typeSpec
		operation.parser.registerTypes[k] = typeSpec
	}
	return nil
}

func regNestFields(ps []*packages.Package, nestNames []string, pkgName string, typeSpecMap map[string]*ast.TypeSpec) {
	for i := range ps {
		for j := range ps[i].Syntax {
			for _, astDeclaration := range ps[i].Syntax[j].Decls {
				if generalDeclaration, ok := astDeclaration.(*ast.GenDecl); ok && generalDeclaration.Tok == token.TYPE {
					for _, astSpec := range generalDeclaration.Specs {
						if typeSpec, ok := astSpec.(*ast.TypeSpec); ok {
							for h := range nestNames {
								if typeSpec.Name.String() == nestNames[h] {
									if _, ok := typeSpecMap[pkgName+"."+nestNames[h]]; !ok {
										//Println(" find typeSpec ", pkgName+"."+nestNames[h])
										typeSpecMap[pkgName+"."+nestNames[h]] = typeSpec

										// 内部的记录
										if t, ok := typeSpec.Type.(*ast.StructType); ok {
											for _, field := range t.Fields.List {
												switch t := field.Type.(type) {
												case *ast.Ident:
													nestNames = append(nestNames, t.Name)
												case *ast.ArrayType:
													nestNames = append(nestNames, TransToValidSchemeType(fmt.Sprintf("%s", t.Elt)))
												default:
													continue
												}
												regNestFields(ps, nestNames, pkgName, typeSpecMap)
											}
										}
									}
									break
								}
							}
						}
					}
				}
			}
		}
	}
}

var regexAttributes = map[string]*regexp.Regexp{
	// for Enums(A, B)
	"enums": regexp.MustCompile(`(?i)enums\(.*\)`),
	// for Minimum(0)
	"maxinum": regexp.MustCompile(`(?i)maxinum\(.*\)`),
	// for Maximum(0)
	"mininum": regexp.MustCompile(`(?i)mininum\(.*\)`),
	// for Maximum(0)
	"default": regexp.MustCompile(`(?i)default\(.*\)`),
	// for minlength(0)
	"minlength": regexp.MustCompile(`(?i)minlength\(.*\)`),
	// for maxlength(0)
	"maxlength": regexp.MustCompile(`(?i)maxlength\(.*\)`),
	// for format(email)
	"format": regexp.MustCompile(`(?i)format\(.*\)`),
	// for example(0)
	"example": regexp.MustCompile(`(?i)example\(.*\)`),
}

func GetAllExtraction(commentLine string) string {
	var result strings.Builder
	for _, re := range regexAttributes {
		findResult := re.FindString(commentLine)
		if findResult != "" {
			result.WriteString(findResult)
			result.WriteString("   ")
		}
	}
	return result.String()
}

func (operation *Operation) parseAndExtractionParamAttribute(commentLine, schemaType string, param *spec.Parameter) error {
	schemaType = TransToValidSchemeType(schemaType)
	for attrKey, re := range regexAttributes {
		switch attrKey {
		case "enums":
			enums, err := findAttrList(re, commentLine)
			if err != nil {
				break
			}
			for _, e := range enums {
				e = strings.TrimSpace(e)

				value, err := defineType(schemaType, e)
				if err != nil {
					return err
				}
				param.Enum = append(param.Enum, value)
			}
		case "maxinum":
			attr, err := findAttr(re, commentLine)
			if err != nil {
				break
			}
			if schemaType != "integer" && schemaType != "number" {
				return fmt.Errorf("maxinum is attribute to set to a number. comment=%s got=%s", commentLine, schemaType)
			}
			n, err := strconv.ParseFloat(attr, 64)
			if err != nil {
				return fmt.Errorf("maximum is allow only a number. comment=%s got=%s", commentLine, attr)
			}
			param.Maximum = &n
		case "mininum":
			attr, err := findAttr(re, commentLine)
			if err != nil {
				break
			}
			if schemaType != "integer" && schemaType != "number" {
				return fmt.Errorf("mininum is attribute to set to a number. comment=%s got=%s", commentLine, schemaType)
			}
			n, err := strconv.ParseFloat(attr, 64)
			if err != nil {
				return fmt.Errorf("mininum is allow only a number got=%s", attr)
			}
			param.Minimum = &n
		case "default":
			attr, err := findAttr(re, commentLine)
			if err != nil {
				break
			}
			value, err := defineType(schemaType, attr)
			if err != nil {
				return nil
			}
			param.Default = value
		case "example":
			attr, err := findAttr(re, commentLine)
			if err != nil {
				break
			}
			value, err := defineTypeOfExample(schemaType, "", attr)
			if err != nil {
				return nil
			}
			param.Example = value
		case "maxlength":
			attr, err := findAttr(re, commentLine)
			if err != nil {
				break
			}
			if schemaType != "string" {
				return fmt.Errorf("maxlength is attribute to set to a number. comment=%s got=%s", commentLine, schemaType)
			}
			n, err := strconv.ParseInt(attr, 10, 64)
			if err != nil {
				return fmt.Errorf("maxlength is allow only a number got=%s", attr)
			}
			param.MaxLength = &n
		case "minlength":
			attr, err := findAttr(re, commentLine)
			if err != nil {
				break
			}
			if schemaType != "string" {
				return fmt.Errorf("maxlength is attribute to set to a number. comment=%s got=%s", commentLine, schemaType)
			}
			n, err := strconv.ParseInt(attr, 10, 64)
			if err != nil {
				return fmt.Errorf("minlength is allow only a number got=%s", attr)
			}
			param.MinLength = &n
		case "format":
			attr, err := findAttr(re, commentLine)
			if err != nil {
				break
			}
			param.Format = attr
		}
	}
	return nil
}

func findAttr(re *regexp.Regexp, commentLine string) (string, error) {
	attr := re.FindString(commentLine)
	l := strings.Index(attr, "(")
	r := strings.Index(attr, ")")
	if l == -1 || r == -1 {
		return "", fmt.Errorf("can not find regex=%s, comment=%s", re.String(), commentLine)
	}
	return strings.TrimSpace(attr[l+1 : r]), nil
}

func findAttrList(re *regexp.Regexp, commentLine string) ([]string, error) {
	attr, err := findAttr(re, commentLine)
	if err != nil {
		return []string{""}, err
	}
	return strings.Split(attr, ","), nil
}

// defineType enum value define the type (object and array unsupported)
func defineType(schemaType string, value string) (interface{}, error) {
	schemaType = TransToValidSchemeType(schemaType)
	switch schemaType {
	case "string":
		return value, nil
	case "number":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("enum value %s can't convert to %s err: %s", value, schemaType, err)
		}
		return v, nil
	case "integer":
		v, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("enum value %s can't convert to %s err: %s", value, schemaType, err)
		}
		return v, nil
	case "boolean":
		v, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("enum value %s can't convert to %s err: %s", value, schemaType, err)
		}
		return v, nil
	default:
		return nil, fmt.Errorf("%s is unsupported type in enum value", schemaType)
	}
}

// ParseTagsComment parses comment for given `tag` comment string.
func (operation *Operation) ParseTagsComment(commentLine string) {
	tags := strings.Split(commentLine, ",")
	for _, tag := range tags {
		operation.Tags = append(operation.Tags, strings.TrimSpace(tag))
	}
}

// ParseAcceptComment parses comment for given `accept` comment string.
func (operation *Operation) ParseAcceptComment(commentLine string) error {
	return parseMimeTypeList(commentLine, &operation.Consumes, "%v accept type can't be accepted")
}

// ParseProduceComment parses comment for given `produce` comment string.
func (operation *Operation) ParseProduceComment(commentLine string) error {
	return parseMimeTypeList(commentLine, &operation.Produces, "%v produce type can't be accepted")
}

// parseMimeTypeList parses a list of MIME Types for a comment like
// `produce` (`Content-Type:` response header) or
// `accept` (`Accept:` request header)
func parseMimeTypeList(mimeTypeList string, typeList *[]string, format string) error {
	mimeTypes := strings.Split(mimeTypeList, ",")
	for _, typeName := range mimeTypes {
		if mimeTypePattern.MatchString(typeName) {
			*typeList = append(*typeList, typeName)
			continue
		}
		if aliasMimeType, ok := mimeTypeAliases[typeName]; ok {
			*typeList = append(*typeList, aliasMimeType)
			continue
		}
		return fmt.Errorf(format, typeName)
	}
	return nil
}

var routerPattern = regexp.MustCompile(`([\w\.\/\-{}\+]+)[^\[]+\[([^\]]+)`)

// ParseRouterComment parses comment for gived `router` comment string.
func (operation *Operation) ParseRouterComment(commentLine string) error {
	var matches []string

	if matches = routerPattern.FindStringSubmatch(commentLine); len(matches) != 3 {
		return fmt.Errorf("can not parse router comment \"%s\"", commentLine)
	}
	path := matches[1]
	httpMethod := matches[2]

	operation.Path = path
	operation.HTTPMethod = strings.ToUpper(httpMethod)

	return nil
}

// ParseSecurityComment parses comment for gived `security` comment string.
func (operation *Operation) ParseSecurityComment(commentLine string) error {
	securitySource := commentLine[strings.Index(commentLine, "@Security")+1:]
	l := strings.Index(securitySource, "[")
	r := strings.Index(securitySource, "]")
	// exists scope
	if !(l == -1 && r == -1) {
		scopes := securitySource[l+1 : r]
		s := []string{}
		for _, scope := range strings.Split(scopes, ",") {
			scope = strings.TrimSpace(scope)
			s = append(s, scope)
		}
		securityKey := securitySource[0:l]
		securityMap := map[string][]string{}
		securityMap[securityKey] = append(securityMap[securityKey], s...)
		operation.Security = append(operation.Security, securityMap)
	} else {
		securityKey := strings.TrimSpace(securitySource)
		securityMap := map[string][]string{}
		securityMap[securityKey] = []string{}
		operation.Security = append(operation.Security, securityMap)
	}
	return nil
}

// 取消这个函数
//// findTypeDef attempts to find the *ast.TypeSpec for a specific type given the
//// type's name and the package's import path
//// TODO: improve finding external pkg
//func findTypeDef(importPath, typeName string) ([]*ast.TypeSpec, error) {
//	typeSpecs := make([]*ast.TypeSpec, 0)
//	return typeSpecs, nil
//}

func (operation *Operation) ParseSingleComment(commentLine string) error {
	mathches := regexp.MustCompile(`\s{3,}`).Split(commentLine, -1)

	if len(mathches) != 2 {
		return errors.New(" Parse Error : Check you param len in : " + commentLine)
	}

	refType := mathches[0]
	desc := mathches[1]

	var response spec.Response
	response.Description = desc
	response.Schema = &spec.Schema{
		SchemaProps: spec.SchemaProps{
			Type: []string{TransToValidSchemeType(refType)},
		},
	}

	if operation.Responses == nil {
		operation.Responses = &spec.Responses{
			ResponsesProps: spec.ResponsesProps{
				StatusCodeResponses: make(map[int]spec.Response),
			},
		}
	}

	operation.Responses.StatusCodeResponses[200] = response

	return nil
}

func (operation *Operation) ParseFailComment(commentLine string, astFile *ast.File) error {
	var matches []string
	matches = regexp.MustCompile(`\s{3,}`).Split(commentLine, -1)

	var sCode, selfCode int
	var err error
	var comment string
	if len(matches) == 2 {
		sCode, err = strconv.Atoi(matches[0])
		comment = matches[1]
		if err != nil {
			return err
		}
	}

	if len(matches) == 3 {
		sCode, err = strconv.Atoi(matches[0])
		if err != nil {
			return err
		}
		m1 := strings.TrimLeft(matches[1], "[")
		m1 = strings.TrimRight(m1, "]")
		selfCode, err = strconv.Atoi(m1)
		if err != nil {
			return err
		}
		comment = matches[2]
		_ = selfCode
	}

	response := spec.Response{}

	if comment == "" {
		response.Description = http.StatusText(sCode)
	} else {
		if selfCode != 0 {
			response.Description = "自定状态码：" + "_" + strconv.Itoa(selfCode) + "_" + "  " + comment
		} else {
			response.Description = comment
		}
	}

	if operation.Responses == nil {
		operation.Responses = &spec.Responses{
			ResponsesProps: spec.ResponsesProps{
				StatusCodeResponses: make(map[int]spec.Response),
			},
		}
	}

	operation.Responses.StatusCodeResponses[sCode] = response

	return nil
}

var responsePattern = regexp.MustCompile(`([\d]+)[\s]+([\w\{\}]+)[\s]+([\w\-\.\/]+)[^"]*(.*)?`)

func (operation *Operation) findResponseMatches(matches []string) (name string, schemaType string, require bool, desc string) {
	if len(matches) < 2 {
		if len(matches) == 1 {
			if matches[0] == "-" {
				return "EndStruct", "", false, ""
			}
		}
		return "", "", false, ""
	}

	name = matches[0]
	schemaType = matches[1]
	desc = matches[len(matches)-1]
	require = true

	if len(matches) == 4 {
		require, _ = strconv.ParseBool(matches[2])
	}
	return
}

// ParseResponseComment parses comment for gived `response` comment string.
func (operation *Operation) ParseResponseComment(commentLine string, astFile *ast.File) error {
	Println(" start parse ... ", commentLine)
	name, schemaType, required, description := operation.findResponseMatches(regexp.MustCompile(`\s{3,}`).Split(commentLine, -1))
	if len(name) == 0 {
		return errors.New(" Parse Error : Check you param len in : " + commentLine)
	}
	var response spec.Response
	response.Description = description
	// so we have to know all type in app
	response.Schema = &spec.Schema{
		SchemaProps: spec.SchemaProps{
			Type: []string{schemaType},
		},
	}

	if name == "{object}" {
		if err := operation.registerSchemaType(schemaType, astFile); err != nil {
			return err
		}

		response.Schema.Required = []string{strconv.FormatBool(required)}
		response.Schema.Type = []string{"object"}
		response.Schema.Ref = spec.Ref{
			Ref: jsonreference.MustCreateRef("#/definitions/" + schemaType),
		}

		if operation.Responses == nil {
			operation.Responses = &spec.Responses{
				ResponsesProps: spec.ResponsesProps{
					StatusCodeResponses: make(map[int]spec.Response),
				},
			}
		}

		operation.Responses.StatusCodeResponses[200] = response
		return nil
	}

	if name == "{array}" {
		if err := operation.registerSchemaType(schemaType, astFile); err != nil {
			return err
		}
		response.Schema.Required = []string{strconv.FormatBool(required)}
		response.Schema.Type = []string{"array"}

		schemaType = TransToValidSchemeType(schemaType)
		if IsPrimitiveType(schemaType) {
			response.Schema.Items = &spec.SchemaOrArray{
				Schema: &spec.Schema{
					SchemaProps: spec.SchemaProps{
						Type: spec.StringOrArray{schemaType},
					},
				},
			}
		} else {
			response.Schema.Items = &spec.SchemaOrArray{
				Schema: &spec.Schema{
					SchemaProps: spec.SchemaProps{
						Ref: spec.Ref{Ref: jsonreference.MustCreateRef("#/definitions/" + schemaType)},
					},
				},
			}
		}

		if operation.Responses == nil {
			operation.Responses = &spec.Responses{
				ResponsesProps: spec.ResponsesProps{
					StatusCodeResponses: make(map[int]spec.Response),
				},
			}
		}

		operation.Responses.StatusCodeResponses[200] = response
		return nil
	}

	if strings.HasPrefix(name, "-") || schemaType == "struct" || schemaType == "array_struct" {
		return operation.ParseStruct(schemaType, name, required, description, commentLine)
	}

	if name == "EndStruct" {
		// +++结束+++
		structPrefix := "Response"
		if err := operation.EndTheStruct(structPrefix, astFile); err != nil {
			return err
		}

		response.Description = operation.SP.Desc
		response.Schema.Required = []string{strconv.FormatBool(operation.SP.Required)}
		if operation.SP.IsArray {
			response.Schema.Type = []string{"array"}

			response.Schema.Items = &spec.SchemaOrArray{
				Schema: &spec.Schema{
					SchemaProps: spec.SchemaProps{
						Ref: spec.Ref{Ref: jsonreference.MustCreateRef("#/definitions/" + operation.SP.FullName)},
					},
				},
			}
		} else {
			response.Schema.Type = []string{"object"}
			response.Schema.Ref = spec.Ref{
				Ref: jsonreference.MustCreateRef("#/definitions/" + operation.SP.FullName),
			}
		}

		operation.SP = nil
		operation.LastPto = nil
	}

	if operation.Responses == nil {
		operation.Responses = &spec.Responses{
			ResponsesProps: spec.ResponsesProps{
				StatusCodeResponses: make(map[int]spec.Response),
			},
		}
	}

	operation.Responses.StatusCodeResponses[200] = response
	return nil
}

func (operation *Operation) ParseStruct(schemaType string, name string, required bool, description string, commentLine string) error {
	// Struct 参数
	isArray := strings.HasPrefix(schemaType, "array")
	schemaType = DelArray(schemaType)

	// +++开始+++
	if schemaType == "struct" && !strings.HasPrefix(name, "-") {
		s := &StructPto{
			Name:     name,
			Required: required,
			Desc:     description,
			IsArray:  isArray,
			Fields:   make([]*FieldPto, 0),
		}
		operation.SP = s
		operation.LastPto = s
		return nil
	}

	// +++中间+++
	if strings.HasPrefix(name, "-") {
		level := strings.Count(name, "-")
		f := &FieldPto{
			Name:     name,
			Type:     schemaType,
			Level:    level,
			Required: required,
			Desc:     description,
			IsArray:  isArray,
			Parent:   operation.LastPto,
			Ex:       GetAllExtraction(commentLine),
		}

		if operation.LastPto != nil {
			if level > operation.LastPto.GetLevel() {
				// 级数变深
				// 加在上一级的本身
				// 本级的父母是它上级
				f.Parent = operation.LastPto
				operation.LastPto.Add(f)
			} else if level == operation.LastPto.GetLevel() {
				// 级数平级
				// 加在上一级的父母上
				// 本级的父母是它上级的父母
				f.Parent = operation.LastPto.GetParent()
				operation.LastPto.AddParent(f)
			} else if level < operation.LastPto.GetLevel() {
				// 级数变小
				// 识别级数差 n
				// 加在上一级的(父母*n)
				// 本级的父母是它上级的(父母*n+1)

				n := operation.LastPto.GetLevel() - level
				lp := operation.LastPto
				for i := 0; i < n; i++ {
					lp = lp.GetParent()
				}

				f.Parent = lp.GetParent()
				lp.AddParent(f)
			}
		}
		operation.LastPto = f
	}
	return nil
}

func (operation *Operation) EndTheStruct(structPrefix string, srcAstFile *ast.File) error {
	if operation.SP != nil {
		// 清空SP ，注册Struct
		// 引用这个虚拟的Model
		fset := token.NewFileSet()
		gFile := operation.SP.Map2GoFile(structPrefix, srcAstFile, operation)
		astFile, err := goparser.ParseFile(fset, "", gFile, goparser.ParseComments)
		if err != nil {
			return err
		}

		pkgName := "swagauto"
		typeName := operation.SP.Name
		pto := operation.SP.FindSPOrChildSPBy(typeName)
		if pto != nil {
			if pto.ShouldAutoPkgFunc() {
				pkgName += GetNonceInt(4)
			}
		}
		for _, astDeclaration := range astFile.Decls {
			if generalDeclaration, ok := astDeclaration.(*ast.GenDecl); ok && generalDeclaration.Tok == token.TYPE {
				for _, astSpec := range generalDeclaration.Specs {
					if typeSpec, ok := astSpec.(*ast.TypeSpec); ok {
						//Printf(" -- registerTempType -- %s", pkgName+"."+typeSpec.Name.String())
						if _, ok := operation.parser.TypeDefinitions[pkgName]; !ok {
							operation.parser.TypeDefinitions[pkgName] = make(map[string]*ast.TypeSpec)
						}

						operation.parser.TypeDefinitions[pkgName][typeSpec.Name.String()] = typeSpec
						operation.parser.registerTypes[pkgName+"."+typeSpec.Name.String()] = typeSpec
					}
				}
			}
		}
		operation.SP.FullName = pkgName + "." + TransToValidSchemeType(typeName)
	}
	return nil
}

func GetNonceInt(n int) string {
	chars := []byte("0123456789")
	value := []byte{}
	m := len(chars)
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for i := 0; i < n; i++ {
		value = append(value, chars[r.Intn(m)])
	}
	return string(value)
}

// ParseResponseHeaderComment parses comment for gived `response header` comment string.
func (operation *Operation) ParseResponseHeaderComment(commentLine string, astFile *ast.File) error {
	var matches []string

	if matches = responsePattern.FindStringSubmatch(commentLine); len(matches) != 5 {
		return fmt.Errorf("can not parse response comment \"%s\"", commentLine)
	}

	response := spec.Response{}

	code, _ := strconv.Atoi(matches[1])

	responseDescription := strings.Trim(matches[4], "\"")
	if responseDescription == "" {
		responseDescription = http.StatusText(code)
	}
	response.Description = responseDescription

	schemaType := strings.Trim(matches[2], "{}")
	refType := matches[3]

	if operation.Responses == nil {
		operation.Responses = &spec.Responses{
			ResponsesProps: spec.ResponsesProps{
				StatusCodeResponses: make(map[int]spec.Response),
			},
		}
	}

	response, responseExist := operation.Responses.StatusCodeResponses[code]
	if responseExist {
		header := spec.Header{}
		header.Description = responseDescription
		header.Type = schemaType

		if response.Headers == nil {
			response.Headers = make(map[string]spec.Header)
		}
		response.Headers[refType] = header

		operation.Responses.StatusCodeResponses[code] = response
	}

	return nil
}

var emptyResponsePattern = regexp.MustCompile(`([\d]+)[\s]+"(.*)"`)

// ParseEmptyResponseComment parse only comment out status code and description,eg: @Success 200 "it's ok"
func (operation *Operation) ParseEmptyResponseComment(commentLine string) error {
	var matches []string

	if matches = emptyResponsePattern.FindStringSubmatch(commentLine); len(matches) != 3 {
		return fmt.Errorf("can not parse response comment \"%s\"", commentLine)
	}

	response := spec.Response{}

	code, _ := strconv.Atoi(matches[1])

	response.Description = strings.Trim(matches[2], "")

	if operation.Responses == nil {
		operation.Responses = &spec.Responses{
			ResponsesProps: spec.ResponsesProps{
				StatusCodeResponses: make(map[int]spec.Response),
			},
		}
	}

	operation.Responses.StatusCodeResponses[code] = response

	return nil
}

//ParseEmptyResponseOnly parse only comment out status code ,eg: @Success 200
func (operation *Operation) ParseEmptyResponseOnly(commentLine string) error {
	response := spec.Response{}

	code, err := strconv.Atoi(commentLine)
	if err != nil {
		return fmt.Errorf("can not parse response comment \"%s\"", commentLine)
	}
	if operation.Responses == nil {
		operation.Responses = &spec.Responses{
			ResponsesProps: spec.ResponsesProps{
				StatusCodeResponses: make(map[int]spec.Response),
			},
		}
	}

	operation.Responses.StatusCodeResponses[code] = response

	return nil
}

// createParameter returns swagger spec.Parameter for gived  paramType, description, paramName, schemaType, required
func createParameter(paramType, description, paramName, schemaType string, required bool) spec.Parameter {
	paramProps := spec.ParamProps{
		Name:        paramName,
		Description: description,
		Required:    required,
		In:          paramType,
	}

	if strings.HasPrefix(schemaType, "array_") {
		// 数组型
		if paramType == "body" {
			// 引用型
			paramProps.Schema = &spec.Schema{
				SchemaProps: spec.SchemaProps{
					Type: []string{"array"},
					Items: &spec.SchemaOrArray{
						Schema: &spec.Schema{
							SchemaProps: spec.SchemaProps{
								Ref: spec.Ref{
									Ref: jsonreference.MustCreateRef("#/definitions/" + DelArray(schemaType)),
								},
							},
						},
					},
				},
			}
			parameter := spec.Parameter{
				ParamProps: paramProps,
			}
			return parameter
		} else {
			parameter := spec.Parameter{
				ParamProps: paramProps,
				SimpleSchema: spec.SimpleSchema{
					Type: "array",
					Items: &spec.Items{
						SimpleSchema: spec.SimpleSchema{
							Type: DelArray(schemaType),
						},
					},
				},
			}
			return parameter
		}
	} else {
		// 普通型
		if paramType == "body" {
			// 引用型
			paramProps.Schema = &spec.Schema{
				SchemaProps: spec.SchemaProps{
					Ref: spec.Ref{
						Ref: jsonreference.MustCreateRef("#/definitions/" + schemaType),
					},
				},
			}

			parameter := spec.Parameter{
				ParamProps: paramProps,
			}
			return parameter
		} else {
			// 非引用型
			parameter := spec.Parameter{
				ParamProps: paramProps,
				SimpleSchema: spec.SimpleSchema{
					Type: schemaType,
				},
			}
			return parameter
		}
	}
}

// array_string => string
func DelArray(s string) string {
	return strings.TrimLeft(s, "array_")
}
