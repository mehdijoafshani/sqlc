package postgresql

import (
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	"github.com/kyleconroy/sqlc/internal/sql/ast"

	pg "github.com/lfittl/pg_query_go"
	nodes "github.com/lfittl/pg_query_go/nodes"
)

func stringSlice(list nodes.List) []string {
	items := []string{}
	for _, item := range list.Items {
		if n, ok := item.(nodes.String); ok {
			items = append(items, n.Str)
		}
	}
	return items
}

func parseTableName(node nodes.Node) (*ast.TableName, error) {
	switch n := node.(type) {

	case nodes.List:
		parts := stringSlice(n)
		switch len(parts) {
		case 1:
			return &ast.TableName{
				Name: parts[0],
			}, nil
		case 2:
			return &ast.TableName{
				Schema: parts[0],
				Name:   parts[1],
			}, nil
		case 3:
			return &ast.TableName{
				Catalog: parts[0],
				Schema:  parts[1],
				Name:    parts[2],
			}, nil
		default:
			return nil, fmt.Errorf("invalid table name: %s", join(n, "."))
		}

	case nodes.RangeVar:
		name := ast.TableName{}
		if n.Catalogname != nil {
			name.Catalog = *n.Catalogname
		}
		if n.Schemaname != nil {
			name.Schema = *n.Schemaname
		}
		if n.Relname != nil {
			name.Name = *n.Relname
		}
		return &name, nil

	default:
		return nil, fmt.Errorf("unexpected node type: %T", n)
	}
}

func join(list nodes.List, sep string) string {
	return strings.Join(stringSlice(list), sep)
}

func NewParser() *Parser {
	return &Parser{}
}

type Parser struct {
}

func (p *Parser) Parse(r io.Reader) ([]ast.Statement, error) {
	contents, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	tree, err := pg.Parse(string(contents))
	if err != nil {
		return nil, err
	}

	var stmts []ast.Statement
	for _, stmt := range tree.Statements {
		raw, ok := stmt.(nodes.RawStmt)
		if !ok {
			return nil, fmt.Errorf("expected RawStmt; got %T", stmt)
		}
		n, err := translate(raw.Stmt)
		if err != nil {
			return nil, err
		}
		if n != nil {
			stmts = append(stmts, ast.Statement{
				Raw: &ast.RawStmt{Stmt: n},
			})
		}
	}
	return stmts, nil
}

func translate(node nodes.Node) (ast.Node, error) {
	switch n := node.(type) {

	case nodes.AlterTableStmt:
		name, err := parseTableName(*n.Relation)
		if err != nil {
			return nil, err
		}
		at := &ast.AlterTableStmt{
			Table: name,
			Cmds:  &ast.List{},
		}
		for _, cmd := range n.Cmds.Items {
			switch cmd := cmd.(type) {
			case nodes.AlterTableCmd:
				item := &ast.AlterTableCmd{Name: cmd.Name, MissingOk: cmd.MissingOk}

				switch cmd.Subtype {
				case nodes.AT_AddColumn:
					d := cmd.Def.(nodes.ColumnDef)
					item.Subtype = ast.AT_AddColumn
					item.Def = &ast.ColumnDef{
						Colname:   *d.Colname,
						TypeName:  &ast.TypeName{Name: join(d.TypeName.Names, ".")},
						IsNotNull: isNotNull(d),
					}

				case nodes.AT_AlterColumnType:
					d := cmd.Def.(nodes.ColumnDef)
					item.Subtype = ast.AT_AlterColumnType
					item.Def = &ast.ColumnDef{
						Colname:   *d.Colname,
						TypeName:  &ast.TypeName{Name: join(d.TypeName.Names, ".")},
						IsNotNull: isNotNull(d),
					}

				case nodes.AT_DropColumn:
					item.Subtype = ast.AT_DropColumn

				case nodes.AT_DropNotNull:
					item.Subtype = ast.AT_DropNotNull

				case nodes.AT_SetNotNull:
					item.Subtype = ast.AT_SetNotNull

				default:
					continue
				}

				at.Cmds.Items = append(at.Cmds.Items, item)
			}
		}
		return at, nil

	case nodes.CreateStmt:
		name, err := parseTableName(*n.Relation)
		if err != nil {
			return nil, err
		}
		create := &ast.CreateTableStmt{
			Name:        name,
			IfNotExists: n.IfNotExists,
		}
		for _, elt := range n.TableElts.Items {
			switch n := elt.(type) {
			case nodes.ColumnDef:
				create.Cols = append(create.Cols, &ast.ColumnDef{
					Colname:   *n.Colname,
					TypeName:  &ast.TypeName{Name: join(n.TypeName.Names, ".")},
					IsNotNull: isNotNull(n),
				})
			}
		}
		return create, nil

	case nodes.DropStmt:
		drop := &ast.DropTableStmt{
			IfExists: n.MissingOk,
		}
		for _, obj := range n.Objects.Items {
			if n.RemoveType == nodes.OBJECT_TABLE {
				name, err := parseTableName(obj)
				if err != nil {
					return nil, err
				}
				drop.Tables = append(drop.Tables, name)
			}
		}
		return drop, nil

	default:
		return nil, nil
	}
}
