// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package optional

import "strconv"

type Option[T any] []T

func None[T any]() Option[T] {
	return nil
}

func Some[T any](v T) Option[T] {
	return Option[T]{v}
}

func FromPtr[T any](v *T) Option[T] {
	if v == nil {
		return None[T]()
	}
	return Some(*v)
}

func FromNonDefault[T comparable](v T) Option[T] {
	var zero T
	if v == zero {
		return None[T]()
	}
	return Some(v)
}

func (o Option[T]) Has() bool {
	return o != nil
}

func (o Option[T]) Get() (has bool, value T) {
	if o != nil {
		has = true
		value = o[0]
	}
	return has, value
}

func (o Option[T]) ValueOrZeroValue() T {
	var zeroValue T
	return o.ValueOrDefault(zeroValue)
}

func (o Option[T]) ValueOrDefault(v T) T {
	if o.Has() {
		return o[0]
	}
	return v
}

// ParseBool get the corresponding optional.Option[bool] of a string using strconv.ParseBool
func ParseBool(s string) Option[bool] {
	v, e := strconv.ParseBool(s)
	if e != nil {
		return None[bool]()
	}
	return Some(v)
}
