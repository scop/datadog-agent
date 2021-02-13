#
# Copyright:: Chef Software, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#

require './lib/cmake.rb'

name "libsolv"
default_version "0.7.4"

# dependency "cmake"
dependency "glibc"
dependency "libxml2"
dependency "popt"
dependency "zlib"
dependency "liblzma"
dependency "zstd"
dependency "bzip2"
dependency "rpm"

license "BSD"
license_file "LICENSE.BSD"
skip_transitive_dependency_licensing true

version("0.7.4") { source sha256: "725b598075c5c44a90cbda5594fd29417144ff8acb9762996162e7b68f94c8c4" }

source url: "https://github.com/rpm-software-management/libsolv/archive/#{version}.tar.gz"

relative_path "libsolv-#{version}"

build do
  env = with_standard_compiler_flags(with_embedded_path)

  patch source: "0001-Allow-not-building-the-CLI-tools.patch", env: env

  cmake_options = [
    "-DENABLE_TOOLS=OFF",
    "-DENABLE_RPMMD=ON",
    "-DWITH_LIBXML2=ON",
    "-DENABLE_COMPLEX_DEPS=ON",
    "-DENABLE_RPMDB=ON",
    "-DENABLE_RPMDB_LIBRPM=ON",
    "-DENABLE_RPMDB_BYRPMHEADER=ON",
    "-DLIB=lib",
  ]

  cmake(*cmake_options, env: env)
end
