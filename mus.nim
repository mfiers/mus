

import std/[os, strutils, terminal, json, times, options]
import argparse
import std/streams
import nativesockets
import memo
import zip/gzipfiles

{.push hints:on.}

const IGNORE_HIST = @[
  "pwd ",
  "ls ",
  "head ",
  "tail ",
  "cd "
  ]


# get all configuration data
proc getConfig(): JsonNode {.memoized.} =

  var cwd = getCurrentDir()
  var retconf = parseJson("{}")
  var configs = newSeq[string]()

  while cwd.len > 3:
    let confFile = joinPath(cwd, "mus.config")
    if fileExists(confFile):
      configs.add(confFile)
    cwd = parentDir(cwd)

  configs.reverse()

  for confFile in configs:
    let inputfile = newFileStream(confFile, fmRead)
    defer: close(inputfile)
    let thisconfig = parseJson(inputfile)
    for (k, v) in thisconfig.pairs():

      #special processing of tags
      if k == "tag":

        # ensure we have a tag JArray
        if not retconf.contains("tag"):
          retconf["tag"] = % @[]

        # process all incoming tag values.
        for tagval in v:
          let tagvalStr = tagval.getStr()
          if tagvalStr.startsWith('-'):
            let toRemove = % tagvalStr[1 .. len(tagvalStr)-1]
            while retconf["tag"].contains(toRemove):
              let removeElemIndex = retconf["tag"].elems.find(toRemove)
              retconf["tag"].elems.delete( removeElemIndex )
          else:
            if not retconf["tag"].contains(tagval):
              retconf["tag"].add( tagval )
      else:
        retconf[k]=v

  return retconf


# set/unset config key/val
proc unsetConfKV(key: string, val: seq[string]) =

  # sanity check
  if (len(val) > 0 and key != "tag"):
    echo "mus unset <key> <val..> only makes sense for tags"
    echo " . e.g. mus unset tag ..."
    return

  let confFile = joinPath(getCurrentDir(), "mus.config")
  var config: JsonNode
  if not fileExists(confFile):
    return
  let inputfile = newFileStream(confFile, fmRead)
  defer: close(inputfile)
  config = parseJson(inputfile)

  if not config.contains(key):
    return

  if key == "tag":
    if len(val) == 0:
      echo "Remove tags with:"
      echo " . `mus set tag -tagname` to override a tag set in an parent folder"
      echo "or"
      echo " . `must unset tag <tagname>"
      return

    for toRemove in val:
      while config["tag"].contains( % toRemove ):
        let removeElemIndex = config["tag"].elems.find( % toRemove )
        config["tag"].elems.delete( removeElemIndex )
    return

  # anything which is NOT a tag
  config.delete(key)

  let outfile = open(confFile, fmWrite)
  defer: close(outfile)
  outfile.write( $ config )


proc setConfKV(key: string, val: string) =
  let confFile = joinPath(getCurrentDir(), "mus.config")
  var config: JsonNode

  if fileExists(confFile):
    let inputfile = newFileStream(confFile, fmRead)
    config = parseJson(inputfile)
    inputfile.close()

  else:
    config = parseJson("{}")

  if key == "tag":
    if not config.contains("tag"):
      config["tag"] = % @[]

    var taglist: JsonNode
    taglist = config["tag"]

    # ensure we remove the opposite tag
    # with or without a -
    var toRemove: JsonNode
    if val.startsWith("-"):
      toRemove = % val[1 .. len(val)-1]
      if len( $ toRemove ) == 0:
        echo "Invalid tag: ", val
        return
    else:
      toRemove = % ("-" & val)

    while config["tag"].contains(toRemove):
      let removeElemIndex = config["tag"].elems.find(toRemove)
      config["tag"].elems.delete( removeElemIndex )

    if not config["tag"].contains( % val):
      config["tag"].add( % val)

  else:
    config[key] = % val

  let outfile = open(confFile, fmWrite)
  defer: close(outfile)
  outfile.write( $ config )


# extract project from config
proc getProject(): Option[string] =
  let config = getConfig()
  if config.contains("project"):
    return some(config{"project"}.getStr())


# create core of a log message that contains basic fields
proc getBaseMessage(): JsonNode =
  let rv = %* {
    "user": getEnv("USER"),
    "time": epochTime(),
    "host": getHostName(),
    "cwd": getCurrentDir()
  }

  let project = getProject()
  if project.isSome:
    rv{"project"} = % project

  return rv


proc storeHist() =

  let config = getConfig()
  if config{"hist"}.getStr() != "on":
     # "Not storing history"
     return

  if isatty(stdin):
    echo "use: `history -1 | mus hist`"
    return

  let hline = readAll(stdin).strip()
  let hlineSplit = hline.splitWhitespace(maxsplit=2)
  let retcode = parseInt(hlineSplit[0])
  let hcmd = hlineSplit[2]

  let hcmdSpace = hcmd & " "
  for toIgnore in IGNORE_HIST:
    if hcmdSpace.startsWith(toIgnore):
      return

  let logmessage = getBaseMessage()
  logmessage{"type"} = % "history"
  logmessage{"command"} = % hcmd
  logmessage{"returncode"} = % retcode

  var outfile = open("mus.log", fmAppend)
  outfile.writeLine($logmessage)


proc storeLog(message: string) =

  let logmessage = getBaseMessage()
  logmessage{"type"} = % "log"
  logmessage{"message"} = % message

  var outfile = open("mus.log", fmAppend)
  outfile.writeLine($logmessage)


{.pop.}

# Command line parser
# I really like this module(!)
var p = newParser:

  help("Few utilities for reproducible data science")

  command("hist"):
    help("Helper to store last command " &
         " from history - use `history -1 | " &
         "mus hist` in PROMPT_COMMAND")
    run:
      storeHist()

  command("log"):
    help("Store a log message")
    arg("message", nargs = -1)
    run:
      storeLog(join(opts.message, " "))

  command("conf"):
    run:
      let config = getConfig()
      echo config.pretty()

  command("set"):
    help("Store a key/value to config.")
    arg("key")
    arg("val")
    run: setConfKV(opts.key, opts.val)

  command("unset"):
    help("Remove a key from local config.")
    arg("key")
    arg("val", nargs = -1, help="helps unset a tag")
    run: unsetConfKV(opts.key, opts.val)

  command("histon"):
    help("Turn history logging on (recursively).")
    run:
      setConfKV("hist", "on")
  command("histoff"):
    help("Turn history logging off (recursively).")
    run:
      setConfKV("hist", "off")

p.run()



