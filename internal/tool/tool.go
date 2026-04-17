// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package tool is a harness for writing Go tools.
package tool

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"time"
)

// This file is a harness for writing your main function.
// The original version of the file is in golang.org/x/tools/internal/tool.
//
// It adds a method to the Application type
//     Main(name, usage string, args []string)
// which should normally be invoked from a true main as follows:
//     func main() {
//       (&Application{}).Main("myapp", "non-flag-command-line-arg-help", os.Args[1:])
//     }
// It recursively scans the application object for fields with a tag containing
//     `flag:"flagnames" help:"short help text"`
// uses all those fields to build command line flags. It will split flagnames on
// commas and add a flag per name.
// It expects the Application type to have a method
//     Run(context.Context, args...string) error
// which it invokes only after all command line flag processing has been finished.
// If Run returns an error, the error will be printed to stderr and the
// application will quit with a non zero exit status.

// Profile can be embedded in your application struct to automatically
// add command line arguments and handling for the common profiling methods.
type Profile struct {
	CPU    string `flag:"profile.cpu" help:"write CPU profile to this file"`
	Memory string `flag:"profile.mem" help:"write memory profile to this file"`
	Alloc  string `flag:"profile.alloc" help:"write alloc profile to this file"`
	Trace  string `flag:"profile.trace" help:"write trace log to this file"`
	Block  string `flag:"profile.block" help:"write block profile to this file"`
}

// Application is the interface that must be satisfied by an object passed to Main.
type Application interface {
	// Name returns the application's name. It is used in help and error messages.
	Name() string
	// Most of the help usage is automatically generated, this string should only
	// describe the contents of non flag arguments.
	Usage() string
	// ShortHelp returns the one line overview of the command.
	ShortHelp() string
	// DetailedHelp should print a detailed help message. It will only ever be shown
	// when the ShortHelp is also printed, so there is no need to duplicate
	// anything from there.
	// It is passed the flag set so it can print the default values of the flags.
	// It should use the flag sets configured Output to write the help to.
	DetailedHelp(*flag.FlagSet)
	// Run is invoked after all flag processing, and inside the profiling and
	// error handling harness.
	Run(ctx context.Context, args ...string) error
}

type SubCommand interface {
	Parent() string
}

// This is the type returned by CommandLineErrorf, which causes the outer main
// to trigger printing of the command line help.
type commandLineError string

func (e commandLineError) Error() string { return string(e) }

// CommandLineErrorf is like fmt.Errorf except that it returns a value that
// triggers printing of the command line help.
// In general you should use this when generating command line validation errors.
func CommandLineErrorf(message string, args ...any) error {
	return commandLineError(fmt.Sprintf(message, args...))
}

// Main should be invoked directly by main function.
// It will only return if there was no error.  If an error
// was encountered it is printed to standard error and the
// application exits with an exit code of 2.
func Main(ctx context.Context, app Application, args []string) {
	s := flag.NewFlagSet(app.Name(), flag.ExitOnError)
	if err := Run(ctx, s, app, args); err != nil {
		fmt.Fprintf(s.Output(), "%s: %v\n", app.Name(), err)
		if _, printHelp := err.(commandLineError); printHelp {
			// TODO(adonovan): refine this. It causes
			// any command-line error to result in the full
			// usage message, which typically obscures
			// the actual error.
			s.Usage()
		}
		os.Exit(2)
	}
}

// Run is the inner loop for Main; invoked by Main, recursively by
// Run, and by various tests.  It runs the application and returns an
// error.
func Run(ctx context.Context, s *flag.FlagSet, app Application, args []string) (resultErr error) {
	p := prepareFlags(s, app, nil)
	return run(ctx, s, app, p, args)
}

// RunInherited is like [Run], but additionally registers the flag-tagged
// fields of parent onto s. App's fields are registered first; any of
// parent's flags whose names collide with an already-registered flag are
// silently skipped, so app's declarations win on conflict. Both app and
// parent write to their own pointer targets, so RunInherited can set
// parent-level state (e.g., a verbose flag declared on the parent) when
// the flag appears alongside a subcommand represented by app.
//
// If either app or parent embeds [Profile], profile flags are honored.
// App's [Profile] is preferred when both are present.
func RunInherited(ctx context.Context, s *flag.FlagSet, app, parent Application, args []string) (resultErr error) {
	p := prepareFlags(s, app, parent)
	return run(ctx, s, app, p, args)
}

// prepareFlags sets s.Usage and registers app's flags on s. If parent is
// non-nil, its flags are also registered, skipping any whose names are
// already claimed by app. Returns the [Profile] pointer to use for
// profile setup (app's if present, otherwise parent's).
func prepareFlags(s *flag.FlagSet, app, parent Application) *Profile {
	s.Usage = func() {
		if app.ShortHelp() != "" {
			fmt.Fprintf(s.Output(), "%s\n\nUsage:\n  ", app.ShortHelp())
			if sub, ok := app.(SubCommand); ok && sub.Parent() != "" {
				// Place [flags] after the subcommand name: stdlib flag stops
				// at the first non-flag arg, so a subcommand's own flags
				// (and globals inherited by [RunInherited]) are parsed in
				// this position. Globals may also appear before the
				// subcommand name at the parent's Application stage.
				fmt.Fprintf(s.Output(), "%s %s [flags]", sub.Parent(), app.Name())
			} else {
				fmt.Fprintf(s.Output(), "%s [flags]", app.Name())
			}
			if usage := app.Usage(); usage != "" {
				fmt.Fprintf(s.Output(), " %s", usage)
			}
			fmt.Fprint(s.Output(), "\n")
		}
		app.DetailedHelp(s)
	}
	p := addFlags(s, reflect.StructField{}, reflect.ValueOf(app), false)
	if parent != nil {
		pp := addFlags(s, reflect.StructField{}, reflect.ValueOf(parent), true)
		if p == nil {
			p = pp
		}
	}
	return p
}

// run parses args and dispatches to app.Run, setting up any profiling
// requested via p along the way.
func run(ctx context.Context, s *flag.FlagSet, app Application, p *Profile, args []string) (resultErr error) {
	if err := s.Parse(args); err != nil {
		return err
	}

	if p != nil && p.CPU != "" {
		f, err := os.Create(p.CPU)
		if err != nil {
			return err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			f.Close() // ignore error
			return err
		}
		defer func() {
			pprof.StopCPUProfile()
			if closeErr := f.Close(); resultErr == nil {
				resultErr = closeErr
			}
		}()
	}

	if p != nil && p.Trace != "" {
		f, err := os.Create(p.Trace)
		if err != nil {
			return err
		}
		if err := trace.Start(f); err != nil {
			f.Close() // ignore error
			return err
		}
		defer func() {
			trace.Stop()
			if closeErr := f.Close(); resultErr == nil {
				resultErr = closeErr
			}
			log.Printf("To view the trace, run:\n$ go tool trace view %s", p.Trace)
		}()
	}

	if p != nil && p.Memory != "" {
		f, err := os.Create(p.Memory)
		if err != nil {
			return err
		}
		defer func() {
			runtime.GC() // get up-to-date statistics
			if err := pprof.WriteHeapProfile(f); err != nil {
				log.Printf("Writing memory profile: %v", err)
			}
			if err := f.Close(); err != nil {
				log.Printf("Closing memory profile: %v", err)
			}
		}()
	}

	if p != nil && p.Alloc != "" {
		f, err := os.Create(p.Alloc)
		if err != nil {
			return err
		}
		defer func() {
			if err := pprof.Lookup("allocs").WriteTo(f, 0); err != nil {
				log.Printf("Writing alloc profile: %v", err)
			}
			if err := f.Close(); err != nil {
				log.Printf("Closing alloc profile: %v", err)
			}
		}()
	}

	if p != nil && p.Block != "" {
		f, err := os.Create(p.Block)
		if err != nil {
			return err
		}
		runtime.SetBlockProfileRate(1) // record all blocking events
		defer func() {
			if err := pprof.Lookup("block").WriteTo(f, 0); err != nil {
				log.Printf("Writing block profile: %v", err)
			}
			if err := f.Close(); err != nil {
				log.Printf("Closing block profile: %v", err)
			}
		}()
	}

	return app.Run(ctx, s.Args()...)
}

// addFlags scans fields of structs recursively to find things with flag
// tags and adds them to the flag set. If skipExisting is true, any flag
// group whose names are already present in f is silently skipped; this
// lets [RunInherited] register a parent's flags without colliding with
// a subcommand's prior declarations.
func addFlags(f *flag.FlagSet, field reflect.StructField, value reflect.Value, skipExisting bool) *Profile {
	// is it a field we are allowed to reflect on?
	if field.PkgPath != "" {
		return nil
	}
	// now see if is actually a flag
	flagNames, isFlag := field.Tag.Lookup("flag")
	help := field.Tag.Get("help")
	if isFlag {
		nameList := strings.Split(flagNames, ",")
		if skipExisting {
			// A collision on any alias skips the whole group, so a
			// subcommand's declaration fully shadows a parent's.
			for _, n := range nameList {
				if f.Lookup(n) != nil {
					return nil
				}
			}
		}
		// add the main flag
		addFlag(f, value, nameList[0], help)
		if len(nameList) > 1 {
			// and now add any aliases using the same flag value
			fv := f.Lookup(nameList[0]).Value
			for _, flagName := range nameList[1:] {
				f.Var(fv, flagName, help)
			}
		}
		return nil
	}
	// not a flag, but it might be a struct with flags in it
	value = resolve(value.Elem())
	if value.Kind() != reflect.Struct {
		return nil
	}

	// TODO(adonovan): there's no need for this special treatment of Profile:
	// The caller can use f.Lookup("profile.cpu") etc instead.
	p, _ := value.Addr().Interface().(*Profile)
	// go through all the fields of the struct
	for i := 0; i < value.Type().NumField(); i++ {
		child := value.Type().Field(i)
		v := value.Field(i)
		// make sure we have a pointer
		if v.Kind() != reflect.Pointer {
			v = v.Addr()
		}
		// When inheriting (skipExisting), descend only into embedded
		// (anonymous) fields. A named struct field such as
		// Application.Serve is a nested subcommand, not a set of
		// globals, so its flags should not be registered on the outer
		// subcommand's FlagSet.
		if skipExisting && !child.Anonymous {
			ct := child.Type
			if ct.Kind() == reflect.Pointer {
				ct = ct.Elem()
			}
			if ct.Kind() == reflect.Struct {
				continue
			}
		}
		// check if that field is a flag or contains flags
		if fp := addFlags(f, child, v, skipExisting); fp != nil {
			p = fp
		}
	}
	return p
}

func addFlag(f *flag.FlagSet, value reflect.Value, flagName string, help string) {
	switch v := value.Interface().(type) {
	case flag.Value:
		f.Var(v, flagName, help)
	case *bool:
		f.BoolVar(v, flagName, *v, help)
	case *time.Duration:
		f.DurationVar(v, flagName, *v, help)
	case *float64:
		f.Float64Var(v, flagName, *v, help)
	case *int64:
		f.Int64Var(v, flagName, *v, help)
	case *int:
		f.IntVar(v, flagName, *v, help)
	case *string:
		f.StringVar(v, flagName, *v, help)
	case *uint:
		f.UintVar(v, flagName, *v, help)
	case *uint64:
		f.Uint64Var(v, flagName, *v, help)
	default:
		log.Fatalf("field %q of type %T is not assignable to flag.Value", flagName, v)
	}
}

func resolve(v reflect.Value) reflect.Value {
	for {
		switch v.Kind() {
		case reflect.Interface, reflect.Pointer:
			v = v.Elem()
		default:
			return v
		}
	}
}
