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

name "libmodulemd"
default_version "2.9.4"

dependency "rpm"
dependency "glib"
dependency "libyaml"

license "MIT"
license_file "COPYING"
skip_transitive_dependency_licensing true

version("2.9.4") { source sha256: "cb86b1dad4f1578895225ba4ee435dbb7d75262898f69a08507b01759bfc81ab" }

source url: "https://github.com/fedora-modularity/libmodulemd/releases/download/libmodulemd-#{version}/modulemd-#{version}.tar.xz"

relative_path "modulemd-#{version}"

build do
  env = with_standard_compiler_flags(with_embedded_path)

  patch source: "0001-Allow-not-generating-Python-bindings.patch", env: env

  meson_command = [
    "meson",
    "--prefix=#{install_dir}/embedded",
    "--libdir=lib",
    "-Dwith_docs=false",
    "-Ddeveloper_build=false",
    "-Dwith_manpages=disabled",
    "-Dwith_python_bindings=false",
    "-Drpmio=disabled",
    "-Dskip_introspection=true",
    "-Dlibmagic=disabled",
    "builddir",
  ]

  command meson_command.join(" "), env: env

  command "ninja -C builddir", env: env
  command "ninja -C builddir install", env: env
end
