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

name "librepo"
default_version "1.12.1"

dependency "gpgme"
dependency "libassuan"
dependency "libgpg-error"
dependency "glib"
dependency "libxml2"
dependency "curl"
dependency "openssl"
dependency "pcre"

license "LGPL-2.1"
license_file "COPYING"
skip_transitive_dependency_licensing true

version("1.12.1") { source sha256: "b78113f3aeb0d562b034dbeb926609019b7bed27e05c9ab5a584a9938de8da9f" }

source url: "https://github.com/rpm-software-management/librepo/archive/#{version}.tar.gz"

relative_path "librepo-#{version}"

build do
  env = with_standard_compiler_flags(with_embedded_path)

  patch source: "0001-cmake-allow-building-without-Python-bindings.patch", env: env

  cmake_options = [
    "-DENABLE_DOCS=OFF",
    "-DENABLE_TESTS=OFF",
    "-DWITH_ZCHUNK=OFF",
    "-DENABLE_PYTHON=OFF",
    "-DENABLE_PYTHON_TESTS=OFF",
  ]

  cmake(*cmake_options, env: env)
end
