package cli

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pragmataW/graphmaster/internal/index"
	"github.com/pragmataW/graphmaster/internal/query"
)

func newQueryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "query <tool> [args] [flags]",
		Short: "Query the code graph",
		Long: "Run one of the code-graph tools against the current repository and print the\n" +
			"same text the MCP server returns. The index is freshened first.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			names := make([]string, 0, len(query.Tools()))
			for _, t := range query.Tools() {
				names = append(names, t.Name)
			}
			return fmt.Errorf("query: unknown tool %q (available: %s)", args[0], strings.Join(names, ", "))
		},
	}
	for _, t := range query.Tools() {
		cmd.AddCommand(newQueryToolCmd(t))
	}
	return cmd
}

// newQueryToolCmd builds a subcommand whose flags mirror the tool's input
// struct: one flag per json field (underscores become dashes), with the
// primary argument also accepted positionally.
func newQueryToolCmd(t query.Tool) *cobra.Command {
	in := t.NewInput()
	binds := bindInputFlags(in)
	use := t.Name
	args := cobra.NoArgs
	if t.Primary != "" {
		use += " [" + t.Primary + "]"
		args = cobra.ArbitraryArgs
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: t.Description,
		Args:  args,
		RunE: func(cmd *cobra.Command, pos []string) error {
			if len(pos) > 0 {
				if err := setPrimary(in, t.Primary, strings.Join(pos, " ")); err != nil {
					return err
				}
			}
			for _, b := range binds {
				b.apply(cmd)
			}
			return runQuery(cmd.Context(), cmd.OutOrStdout(), ".", t, in)
		},
	}
	if alias := strings.ReplaceAll(t.Name, "_", "-"); alias != t.Name {
		cmd.Aliases = []string{alias}
	}
	for _, b := range binds {
		b.register(cmd)
	}
	return cmd
}

// runQuery opens the index for dir, runs the tool and prints its text.
func runQuery(ctx context.Context, out io.Writer, dir string, t query.Tool, in any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	opts := index.Options{}
	if cliResolver != nil {
		opts.Resolver = cliResolver()
	}
	ix, err := index.Open(ctx, dir, opts)
	if err != nil {
		return err
	}
	defer ix.Close()
	text, err := t.Run(ctx, query.New(ix, query.Options{}), in)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, text)
	return err
}

// flagBind ties one input field to a command-line flag.
type flagBind struct {
	name, usage string
	field       reflect.Value
	// boolPtr holds the parsed value of a *bool field until apply.
	boolPtr bool
}

// bindInputFlags creates a binding for every json-tagged field of the
// struct pointed to by in.
func bindInputFlags(in any) []*flagBind {
	v := reflect.ValueOf(in).Elem()
	var out []*flagBind
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		name := jsonName(f)
		if name == "" {
			continue
		}
		out = append(out, &flagBind{
			name:  strings.ReplaceAll(name, "_", "-"),
			usage: f.Tag.Get("jsonschema"),
			field: v.Field(i),
		})
	}
	return out
}

func (b *flagBind) register(cmd *cobra.Command) {
	fs := cmd.Flags()
	switch p := b.field.Addr().Interface().(type) {
	case *string:
		fs.StringVar(p, b.name, "", b.usage)
	case *int:
		fs.IntVar(p, b.name, 0, b.usage)
	case *bool:
		fs.BoolVar(p, b.name, false, b.usage)
	case *[]string:
		fs.StringSliceVar(p, b.name, nil, b.usage)
	case **bool:
		fs.BoolVar(&b.boolPtr, b.name, false, b.usage)
	}
}

// apply copies values that need post-processing (optional booleans).
func (b *flagBind) apply(cmd *cobra.Command) {
	if p, ok := b.field.Addr().Interface().(**bool); ok && cmd.Flags().Changed(b.name) {
		v := b.boolPtr
		*p = &v
	}
}

func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "-" || !f.IsExported() {
		return ""
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}
	return name
}

// setPrimary stores the positional argument in the field named primary.
func setPrimary(in any, primary, value string) error {
	v := reflect.ValueOf(in).Elem()
	for i := 0; i < v.NumField(); i++ {
		if jsonName(v.Type().Field(i)) == primary {
			if fv := v.Field(i); fv.Kind() == reflect.String {
				fv.SetString(value)
				return nil
			}
		}
	}
	return fmt.Errorf("query: positional argument not supported")
}
