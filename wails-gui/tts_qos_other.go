//go:build !darwin

package main

// runAtUtilityQoS runs fn as it is where no thread class is available.
func runAtUtilityQoS(fn func()) { fn() }
