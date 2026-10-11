; graphmaster extraction query for Python (tree-sitter-python v0.25).
;
; Capture convention: see queries/go.scm. Python specifics handled by the
; hooks in lang_python.go:
;   - functions nested in a class body become methods (__init__/__new__
;     constructors), module-level ALL_CAPS variables constants, classes with
;     an Enum base enums and their fields enum members;
;   - a from-import statement is captured whole and split into one Import
;     per imported name (Spec "module.name", see pythonSpec.postProcess);
;   - receivers rooted at an import-bound name or a same-file class become
;     qualifiers; builtin references are dropped.

; ---------------------------------------------------------------- imports

(import_statement
  name: (dotted_name) @import.path)

(import_statement
  name: (aliased_import
    name: (dotted_name) @import.path
    alias: (identifier) @import.alias))

; The whole statement; split per name by postProcess.
(import_from_statement) @import.path

; ---------------------------------------------------------------- definitions

; A decorated definition spans its decorators; undecorated ones live
; directly in a module or block.
(module
  (function_definition
    name: (identifier) @name) @def.function)

(block
  (function_definition
    name: (identifier) @name) @def.function)

(decorated_definition
  definition: (function_definition
    name: (identifier) @name)) @def.function

(module
  (class_definition
    name: (identifier) @name) @def.class)

(block
  (class_definition
    name: (identifier) @name) @def.class)

(decorated_definition
  definition: (class_definition
    name: (identifier) @name)) @def.class

; Class attributes (annotated or assigned in the class body).
(class_definition
  body: (block
    (expression_statement
      (assignment
        left: (identifier) @name) @def.field)))

; Module-level variables; ALL_CAPS ones become constants in postProcess.
(module
  (expression_statement
    (assignment
      left: (identifier) @name) @def.variable))

; PEP 695 type aliases.
(type_alias_statement
  left: (type (identifier) @name)) @def.type

; ---------------------------------------------------------------- tests
;
; The @test node must be the def node: the decorated_definition when the
; definition is decorated.

; pytest: module-level test* functions.
(module
  (function_definition
    name: (identifier) @_name
    (#match? @_name "^test")) @test)

(module
  (decorated_definition
    definition: (function_definition
      name: (identifier) @_name
      (#match? @_name "^test"))) @test)

; pytest Test* classes and unittest TestCase subclasses.
(module
  (class_definition
    name: (identifier) @_name
    (#match? @_name "^Test")) @test)

(module
  (decorated_definition
    definition: (class_definition
      name: (identifier) @_name
      (#match? @_name "^Test"))) @test)

(module
  (class_definition
    superclasses: (argument_list
      [(identifier) (attribute)] @_base)
    (#match? @_base "TestCase$")) @test)

; test* methods of such classes.
(class_definition
  name: (identifier) @_cls
  (#match? @_cls "^Test")
  body: (block
    (function_definition
      name: (identifier) @_name
      (#match? @_name "^test")) @test))

(class_definition
  name: (identifier) @_cls
  (#match? @_cls "^Test")
  body: (block
    (decorated_definition
      definition: (function_definition
        name: (identifier) @_name
        (#match? @_name "^test"))) @test))

(class_definition
  superclasses: (argument_list
    [(identifier) (attribute)] @_base)
  (#match? @_base "TestCase$")
  body: (block
    (function_definition
      name: (identifier) @_name
      (#match? @_name "^test")) @test))

; ---------------------------------------------------------------- references

(call
  function: (identifier) @name) @ref.call

(call
  function: (attribute
    object: (_) @receiver
    attribute: (identifier) @name)) @ref.call

(attribute
  object: (_) @receiver
  attribute: (identifier) @name) @ref.member

(decorator
  (identifier) @name) @ref.call

(decorator
  (attribute
    object: (_) @receiver
    attribute: (identifier) @name)) @ref.call

; raise Exc (without a call) instantiates the class.
(raise_statement
  (identifier) @name) @ref.call

; Type annotations: plain, generic, dotted and unions (two levels).
(type
  (identifier) @name) @ref.type

(generic_type
  (identifier) @name) @ref.type

(type
  (attribute
    object: (_) @qualifier
    attribute: (identifier) @name)) @ref.type

(type
  (binary_operator
    left: (identifier) @name)) @ref.type

(type
  (binary_operator
    right: (identifier) @name)) @ref.type

(type
  (binary_operator
    left: (binary_operator
      left: (identifier) @name))) @ref.type

(type
  (binary_operator
    left: (binary_operator
      right: (identifier) @name))) @ref.type

; except clauses and isinstance/issubclass checks name types.
(except_clause
  value: (identifier) @name) @ref.type

(except_clause
  value: (attribute
    object: (_) @qualifier
    attribute: (identifier) @name)) @ref.type

(except_clause
  value: (tuple
    (identifier) @name)) @ref.type

(except_clause
  value: (as_pattern
    (identifier) @name)) @ref.type

(except_clause
  value: (as_pattern
    (tuple
      (identifier) @name))) @ref.type

(call
  function: (identifier) @_fn
  (#any-of? @_fn "isinstance" "issubclass")
  arguments: (argument_list
    (_)
    .
    (identifier) @name)) @ref.type

; ---------------------------------------------------------------- inheritance

(class_definition
  superclasses: (argument_list
    [(identifier) (attribute)] @inherit.extends))

(class_definition
  superclasses: (argument_list
    (subscript
      value: (_) @inherit.extends)))
