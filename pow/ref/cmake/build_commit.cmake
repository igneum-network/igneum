# The build commit at BUILD time (the release team's recipe fault 1, 12 Oct 2026: read at configure time, a build directory configured
# once named every later build after the first commit). Run by the ignpow_commit target on every build; the header is rewritten only
# when the commit changes, so an unchanged tree relinks nothing.
execute_process(COMMAND git rev-parse --short=8 HEAD WORKING_DIRECTORY ${SRC} OUTPUT_VARIABLE C OUTPUT_STRIP_TRAILING_WHITESPACE ERROR_QUIET)
if(NOT C)
  set(C "unknown")
endif()
set(LINE "#define IGNPOW_BUILD_COMMIT \"${C}\"\n")
if(EXISTS ${OUT})
  file(READ ${OUT} OLD)
else()
  set(OLD "")
endif()
if(NOT OLD STREQUAL LINE)
  file(WRITE ${OUT} "${LINE}")
endif()
