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

name "apk-tools"
default_version "2.12.0"

dependency 'zlib'
dependency 'openssl'

license "GPLv2"
license_file "LICENSE"
skip_transitive_dependency_licensing true

version("2.12.0") { source sha256: "10ab80784ee9f1a97f55ad333f6190f4ecd690abd56b888ebc8f6d948bd101c1" }

source url: "https://gitlab.alpinelinux.org/alpine/apk-tools/-/archive/v#{version}/apk-tools-v#{version}.tar.bz2"

relative_path "apk-tools-v#{version}"

build do
  env = with_standard_compiler_flags(with_embedded_path)
  env["CFLAGS"] += " -Wno-error=unused-result"

  patch source: "do-not-install-doc.patch"

  make "-j #{workers} LUA=no", env: env
  make "install LUA=no DOC=no DESTDIR=#{install_dir}/embedded", env: env
end
