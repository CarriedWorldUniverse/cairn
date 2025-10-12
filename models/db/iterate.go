// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package db

import (
	"context"
	"fmt"
	"reflect"

	"forgejo.org/modules/setting"

	"xorm.io/builder"
)

// Iterate iterate all the Bean object. The table being iterated must have a single-column primary key.
func Iterate[Bean any](ctx context.Context, cond builder.Cond, f func(ctx context.Context, bean *Bean) error) error {
	var dummy Bean
	batchSize := setting.Database.IterateBufferSize

	table, err := TableInfo(&dummy)
	if err != nil {
		return fmt.Errorf("unable to fetch table info for bean %v: %w", dummy, err)
	}
	if len(table.PrimaryKeys) != 1 {
		return fmt.Errorf("iterate only supported on a table with 1 primary key field, but table %s had %d", table.Name, len(table.PrimaryKeys))
	}

	pkDbName := table.PrimaryKeys[0]
	var pkStructFieldName string

	for _, c := range table.Columns() {
		if c.Name == pkDbName {
			pkStructFieldName = c.FieldName
			break
		}
	}
	if pkStructFieldName == "" {
		return fmt.Errorf("iterate unable to identify struct field for primary key %s", pkDbName)
	}

	var lastPK any

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			beans := make([]*Bean, 0, batchSize)

			sess := GetEngine(ctx)
			sess = sess.OrderBy(pkDbName)
			if cond != nil {
				sess = sess.Where(cond)
			}
			if lastPK != nil {
				sess = sess.Where(builder.Gt{pkDbName: lastPK})
			}

			if err := sess.Limit(batchSize).Find(&beans); err != nil {
				return err
			}
			if len(beans) == 0 {
				return nil
			}

			for _, bean := range beans {
				if err := f(ctx, bean); err != nil {
					return err
				}
			}

			lastBean := beans[len(beans)-1]
			lastPK = extractFieldValue(lastBean, pkStructFieldName)
		}
	}
}

func extractFieldValue(bean any, fieldName string) any {
	v := reflect.ValueOf(bean)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	field := v.FieldByName(fieldName)
	return field.Interface()
}
