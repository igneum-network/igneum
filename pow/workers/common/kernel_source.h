// kernel_source.h: a kernel source by name: the file in the given directory when it is
// there, else the copy embedded at build time (the Windows agent fetches one binary by
// sha, so the kernels travel inside it). GPU engineer B, 10 October 2026.
#pragma once
#include <string>
namespace igm {
std::string kernel_source(const std::string &dir, const std::string &name);
}
