// vectors_main.h: the shared `--vectors` entry: runs each file given, after the
// reference's own self-test, and exits 0 only when every file has 0 mismatches.
#pragma once
#include <string>
#include <vector>
#include "engine_iface.h"

namespace igm {
int vectors_main(EngineIface &eng, const std::vector<std::string> &files);
}
