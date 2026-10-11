; graphmaster extraction query for C++ (tree-sitter-cpp v0.23).
;
; Capture convention: see queries/go.scm; C rules: see queries/c.scm.
; C++ specifics (refined by the spec's postProcess):
;   - functions are all captured as @def.function; the spec turns those
;     nested in a class into methods/constructors and qualified out-of-line
;     definitions (void ns::Cls::m() {}) into members of Cls;
;   - for qualified names the whole qualified_identifier is captured as
;     @scope / @qualifier and the spec strips the trailing name and template
;     arguments ("ns::Cls::m" -> scope ns::Cls);
;   - using-directives / using-declarations are captured whole and decoded by
;     the spec's import cleaner.

; ---------------------------------------------------------------- imports

(preproc_include path: (_) @import.path)
(using_declaration) @import.path
(namespace_alias_definition) @import.path

; ---------------------------------------------------------------- macros

(preproc_def name: (identifier) @name value: (_)) @def.macro

(preproc_def
  name: (identifier) @name
  !value
  (#not-match? @name "(_H|_HH|_HPP|_HXX|_INC|_INCLUDED|_DEFINED)_*$")) @def.macro

(preproc_function_def name: (identifier) @name) @def.macro

; ---------------------------------------------------------------- namespaces

(namespace_definition name: [(namespace_identifier) (nested_namespace_specifier)] @name) @def.namespace

; ---------------------------------------------------------------- types

(class_specifier name: (type_identifier) @name body: (_)) @def.class
(class_specifier name: (template_type name: (type_identifier) @name) body: (_)) @def.class
(struct_specifier name: (type_identifier) @name body: (_)) @def.struct
(struct_specifier name: (template_type name: (type_identifier) @name) body: (_)) @def.struct
(union_specifier name: (type_identifier) @name body: (_)) @def.struct
(enum_specifier name: (type_identifier) @name body: (_)) @def.enum

(enumerator name: (identifier) @name) @def.enum_member

(alias_declaration name: (type_identifier) @name) @def.type

(type_definition
  type: [(struct_specifier !name body: (_)) (union_specifier !name body: (_)) (class_specifier !name body: (_))]
  declarator: (type_identifier) @name) @def.struct

(type_definition
  type: (enum_specifier !name body: (_))
  declarator: (type_identifier) @name) @def.enum

(type_definition
  type: [
    (struct_specifier name: (_) body: (_))
    (union_specifier name: (_) body: (_))
    (class_specifier name: (_) body: (_))
    (enum_specifier name: (_) body: (_))
  ]
  declarator: (type_identifier) @name @def.type)

(type_definition
  type: [
    (primitive_type) (type_identifier) (sized_type_specifier) (qualified_identifier)
    (template_type) (struct_specifier !body) (union_specifier !body)
    (class_specifier !body) (enum_specifier !body)
  ]
  declarator: [
    (type_identifier) @name
    (pointer_declarator declarator: (type_identifier) @name)
    (reference_declarator (type_identifier) @name)
    (array_declarator declarator: (type_identifier) @name)
    (function_declarator declarator: (type_identifier) @name)
    (function_declarator declarator: (parenthesized_declarator
      (pointer_declarator declarator: (type_identifier) @name)))
  ]) @def.type

; ---------------------------------------------------------------- fields

(field_declaration
  declarator: [
    (field_identifier) @name
    (pointer_declarator declarator: (field_identifier) @name)
    (pointer_declarator declarator: (pointer_declarator declarator: (field_identifier) @name))
    (reference_declarator (field_identifier) @name)
    (array_declarator declarator: (field_identifier) @name)
    (pointer_declarator declarator: (array_declarator declarator: (field_identifier) @name))
    (function_declarator declarator: (parenthesized_declarator
      (pointer_declarator declarator: (field_identifier) @name)))
  ]) @def.field

; ---------------------------------------------------------------- functions

; Unqualified names: free functions, inline methods, constructors,
; destructors and operators; up to two pointer/reference levels on the
; return type.
(function_definition
  declarator: [
    (function_declarator declarator: [(identifier) (field_identifier) (destructor_name) (operator_name)] @name)
    (function_declarator declarator: (template_function name: (identifier) @name))
    (pointer_declarator declarator: (function_declarator
      declarator: [(identifier) (field_identifier) (operator_name)] @name))
    (reference_declarator (function_declarator
      declarator: [(identifier) (field_identifier) (operator_name)] @name))
    (pointer_declarator declarator: (pointer_declarator declarator: (function_declarator
      declarator: [(identifier) (field_identifier) (operator_name)] @name)))
  ]
  (#not-any-of? @name "TEST" "TEST_F" "TEST_P" "TYPED_TEST" "TYPED_TEST_P")) @def.function

; Qualified (out-of-line) definitions, up to three qualifier levels.
(function_definition
  declarator: [
    (function_declarator declarator: (qualified_identifier
      name: [(identifier) (destructor_name) (operator_name)] @name) @scope)
    (pointer_declarator declarator: (function_declarator declarator: (qualified_identifier
      name: [(identifier) (destructor_name) (operator_name)] @name) @scope))
    (reference_declarator (function_declarator declarator: (qualified_identifier
      name: [(identifier) (destructor_name) (operator_name)] @name) @scope))
  ]) @def.function

(function_definition
  declarator: [
    (function_declarator declarator: (qualified_identifier name: (qualified_identifier
      name: [(identifier) (destructor_name) (operator_name)] @name)) @scope)
    (pointer_declarator declarator: (function_declarator declarator: (qualified_identifier
      name: (qualified_identifier name: [(identifier) (destructor_name) (operator_name)] @name)) @scope))
    (reference_declarator (function_declarator declarator: (qualified_identifier
      name: (qualified_identifier name: [(identifier) (destructor_name) (operator_name)] @name)) @scope))
  ]) @def.function

(function_definition
  declarator: (function_declarator declarator: (qualified_identifier name: (qualified_identifier
    name: (qualified_identifier name: [(identifier) (destructor_name) (operator_name)] @name))) @scope)) @def.function

; Prototypes and in-class method declarations (flagged as declarations).
(declaration
  declarator: [
    (function_declarator declarator: [(identifier) (field_identifier) (destructor_name) (operator_name)] @name)
    (pointer_declarator declarator: (function_declarator
      declarator: [(identifier) (field_identifier) (operator_name)] @name))
    (reference_declarator (function_declarator
      declarator: [(identifier) (field_identifier) (operator_name)] @name))
    (pointer_declarator declarator: (pointer_declarator declarator: (function_declarator
      declarator: [(identifier) (field_identifier) (operator_name)] @name)))
  ]) @def.function

(field_declaration
  declarator: [
    (function_declarator declarator: [(identifier) (field_identifier) (destructor_name) (operator_name)] @name)
    (pointer_declarator declarator: (function_declarator
      declarator: [(identifier) (field_identifier) (operator_name)] @name))
    (reference_declarator (function_declarator
      declarator: [(identifier) (field_identifier) (operator_name)] @name))
    (pointer_declarator declarator: (pointer_declarator declarator: (function_declarator
      declarator: [(identifier) (field_identifier) (operator_name)] @name)))
  ]) @def.function

; ---------------------------------------------------------------- variables

(declaration
  declarator: [
    (identifier) @name
    (pointer_declarator declarator: (identifier) @name)
    (reference_declarator (identifier) @name)
    (array_declarator declarator: (identifier) @name)
    (pointer_declarator declarator: (array_declarator declarator: (identifier) @name))
    (init_declarator declarator: [
      (identifier) @name
      (pointer_declarator declarator: (identifier) @name)
      (reference_declarator (identifier) @name)
      (array_declarator declarator: (identifier) @name)
      (pointer_declarator declarator: (array_declarator declarator: (identifier) @name))
    ])
  ]) @def.variable

; ---------------------------------------------------------------- tests

; GoogleTest: TEST(Suite, Name) { ... } is a test named Name in Suite.
(function_definition
  declarator: (function_declarator
    declarator: (identifier) @_macro
    parameters: (parameter_list
      .
      (parameter_declaration type: (type_identifier) @scope)
      .
      (parameter_declaration type: (type_identifier) @name)
      .))
  (#any-of? @_macro "TEST" "TEST_F" "TEST_P" "TYPED_TEST" "TYPED_TEST_P")) @def.test @test

; Unity-style free test functions: void test_xxx() / void test_xxx(void).
(function_definition
  type: (primitive_type) @_ret
  declarator: (function_declarator
    declarator: (identifier) @_name
    parameters: (parameter_list) @_params)
  (#eq? @_ret "void")
  (#match? @_name "^test(_|[A-Z])")
  (#match? @_params "^\\(\\s*(void)?\\s*\\)$")) @test

; ---------------------------------------------------------------- locals

; Template type parameters shadow unqualified type references.
(template_parameter_list (type_parameter_declaration (type_identifier) @local))
(template_parameter_list (optional_type_parameter_declaration name: (type_identifier) @local))
(template_parameter_list (variadic_type_parameter_declaration (type_identifier) @local))

; ---------------------------------------------------------------- inheritance

(base_class_clause
  [(type_identifier) (qualified_identifier) (template_type)] @inherit.extends)

; ---------------------------------------------------------------- references

(call_expression function: (identifier) @name) @ref.call
(call_expression function: (template_function name: (identifier) @name)) @ref.call

; Qualified calls ns::f(), Cls::f(), a::b::f<T>(): the outermost
; qualified_identifier is the qualifier (the spec strips the name).
(call_expression
  function: (qualified_identifier
    name: [(identifier) @name (template_function name: (identifier) @name)]) @qualifier) @ref.call

(call_expression
  function: (qualified_identifier name: (qualified_identifier
    name: [(identifier) @name (template_function name: (identifier) @name)])) @qualifier) @ref.call

(call_expression
  function: (qualified_identifier name: (qualified_identifier name: (qualified_identifier
    name: [(identifier) @name (template_function name: (identifier) @name)]))) @qualifier) @ref.call

; Member calls x.f(), p->f(), this->f(), x.template f<T>().
(call_expression
  function: (field_expression
    argument: (_) @receiver
    field: [(field_identifier) @name (template_method name: (field_identifier) @name)])) @ref.call

(field_expression
  argument: (_) @receiver
  field: (field_identifier) @name) @ref.member

; new T(...) calls T's constructor.
(new_expression type: (type_identifier) @name) @ref.call
(new_expression type: (template_type name: (type_identifier) @name)) @ref.call
(new_expression type: (qualified_identifier name: [(type_identifier) @name (template_type name: (type_identifier) @name)]) @qualifier) @ref.call

; Types, unqualified and qualified (outermost qualifier wins).
(type_identifier) @name @ref.type

(qualified_identifier
  name: [(type_identifier) @name (template_type name: (type_identifier) @name)]) @qualifier @ref.type

(qualified_identifier name: (qualified_identifier
  name: [(type_identifier) @name (template_type name: (type_identifier) @name)])) @qualifier @ref.type

(qualified_identifier name: (qualified_identifier name: (qualified_identifier
  name: [(type_identifier) @name (template_type name: (type_identifier) @name)]))) @qualifier @ref.type

; Uses of ALL_CAPS names (object-like macros, constants): best effort.
((identifier) @name @ref.member
  (#match? @name "^[A-Z][A-Z0-9_]*[A-Z0-9]$")
  (#not-any-of? @name "NULL" "EOF" "TRUE" "FALSE" "EXIT_SUCCESS" "EXIT_FAILURE"
    "TEST" "TEST_F" "TEST_P" "TYPED_TEST" "TYPED_TEST_P"))
