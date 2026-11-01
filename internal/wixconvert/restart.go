package wixconvert

import "strings"

const elemRestartResource = "util:RestartResource"

// restartResource converts a util:RestartResource inside a component into
// an entry of its restartResources: Path becomes file and ServiceName
// service. ProcessName has no counterpart, because program takes the
// program's full path, so that copies elsewhere are left alone.
func (cc *compConv) restartResource(elm *elem) {
	conv := cc.c
	res := newMap()
	res.head = comments(elm)

	switch {
	case elm.has("Path"):
		path := conv.value(elm, "Path")
		res.set("file", path)

		if strings.HasSuffix(path, `\`) {
			conv.todof(res, "file", elm.line, "<util:RestartResource> Path %s ends in a backslash; restartResources "+
				"registers a file, so list the files that programs hold open instead of their folder", path)
		}
	case elm.has("ServiceName"):
		res.set("service", conv.value(elm, "ServiceName"))
	case elm.has("ProcessName"):
		conv.todof(cc.out, "", elm.line, "<util:RestartResource> ProcessName %s matches every program of that name; "+
			"restartResources program takes its full path so that copies elsewhere are left alone; Restart Manager "+
			"already shuts down programs that run from a file of the package", conv.value(elm, "ProcessName"))

		return
	default:
		conv.todof(cc.out, "", elm.line, "<util:RestartResource> names no Path, ProcessName or ServiceName")

		return
	}

	cc.out.add("restartResources", res)
}
