package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"supplier-catalog-sync/internal/domain"
	"supplier-catalog-sync/internal/store"
	"supplier-catalog-sync/internal/supplier"
	"supplier-catalog-sync/internal/syncer"
	"syscall"
)

func main() { os.Exit(run()) }
func run() int {
	global := flag.NewFlagSet("catalog", flag.ContinueOnError)
	global.SetOutput(os.Stderr)
	db := global.String("db", "catalog.json", "catalog database")
	if err := global.Parse(os.Args[1:]); err != nil {
		return 2
	}
	args := global.Args()
	if len(args) == 0 {
		usage()
		return 2
	}
	st, err := store.Open(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	eng := &syncer.Engine{Store: st}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "start":
		return start(ctx, eng, args[1:])
	case "resume":
		return resume(ctx, eng, args[1:])
	case "abort":
		r, err := eng.Abort()
		if err != nil {
			return fail(err)
		}
		printRunSummary(r)
		return 0
	case "status":
		r := st.State().LatestRun()
		if r == nil {
			fmt.Println("No updates have run.")
			return 0
		}
		printStatus(*r)
		return 0
	case "report":
		return reportCommand(st, args[1:])
	case "products":
		return productsCommand(st, args[1:])
	default:
		usage()
		return 2
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: catalog [--db catalog.json] <start|resume|abort|status|report|products>")
}
func fail(err error) int { fmt.Fprintln(os.Stderr, "error:", err); return 1 }

func start(ctx context.Context, e *syncer.Engine, args []string) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	file := fs.String("file", "", "supplier feed")
	failAfter := fs.Int("fail-after", -1, "simulate outage after batch")
	crashAfter := fs.Int("crash-after", -1, "exit after committed batch")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		return fail(errors.New("start requires --file"))
	}
	c, err := supplier.OpenFile(*file)
	if err != nil {
		return fail(err)
	}
	c.FailAfter = *failAfter
	setCrashHook(e, *crashAfter)
	r, err := e.Start(ctx, *file, c)
	if errors.Is(err, syncer.ErrRunOpen) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return finish(r, err, e.Store)
}
func resume(ctx context.Context, e *syncer.Engine, args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	failAfter := fs.Int("fail-after", -1, "simulate outage after batch")
	crashAfter := fs.Int("crash-after", -1, "exit after committed batch")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	open := e.Store.State().OpenRun()
	if open == nil {
		return fail(errors.New("no update to resume"))
	}
	c, err := supplier.OpenFile(open.Source)
	if err != nil {
		return fail(err)
	}
	c.FailAfter = *failAfter
	setCrashHook(e, *crashAfter)
	r, err := e.Resume(ctx, c)
	return finish(r, err, e.Store)
}
func setCrashHook(e *syncer.Engine, seq int) {
	if seq >= 0 {
		e.AfterBatch = func(done int) {
			if done == seq {
				fmt.Fprintf(os.Stderr, "simulated crash after committed batch %d\n", done)
				os.Exit(3)
			}
		}
	}
}
func finish(r domain.Run, err error, repository syncer.Repository) int {
	st, ok := repository.(*store.Store)
	if !ok {
		return fail(errors.New("CLI requires the JSON file store"))
	}
	printReport(st, r.ID)
	if errors.Is(err, syncer.ErrIncomplete) {
		fmt.Fprintln(os.Stderr, "Catalog was not damaged. Run `catalog resume` to continue.")
		return 1
	}
	if err != nil {
		return fail(err)
	}
	return 0
}
