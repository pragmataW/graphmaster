; graphmaster extraction query additions for TSX (tree-sitter-typescript
; v0.23, "tsx" dialect). The TSX spec appends this file to typescript.scm.
;
; Using a capitalized JSX element is a call of that component; lowercase
; names are intrinsic HTML elements and are not references.

(jsx_opening_element
  name: (identifier) @name
  (#match? @name "^[A-Z]")) @ref.call

(jsx_self_closing_element
  name: (identifier) @name
  (#match? @name "^[A-Z]")) @ref.call

(jsx_opening_element
  name: (member_expression
    object: (_) @receiver
    property: (property_identifier) @name)) @ref.call

(jsx_self_closing_element
  name: (member_expression
    object: (_) @receiver
    property: (property_identifier) @name)) @ref.call
