#
# Copyright 2012-2014 Chef Software, Inc.
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

name "glibc"
default_version "2.32"

license "LGPL-2.1"
license_file "COPYING.LIB"
skip_transitive_dependency_licensing true

version("2.17") { source sha256: "80f5acd0bbc573ad80579ae98c789143c75f13fb39e4dbd2449c086774b8315c" }
version("2.32") { source sha256: "6d34d8ba95e714dbede304dad8bf8931bf3950293f8c14ab57167ae141aad68a" }

source url: "https://ftp.gnu.org/gnu/glibc/glibc-#{version}.tar.bz2"

relative_path "glibc-#{version}"

build do
  env = with_standard_compiler_flags(with_embedded_path)

  cflags = env['CFLAGS'].split()
  cflags.delete("-D_FORTIFY_SOURCE=2")
  cflags.delete("-fstack-protector")
  cflags << "-Wno-error"
  env['CFLAGS'] = env['CPPFLAGS'] = env['CXXFLAGS'] = cflags.join(" ")

  mkdir "builddir"
  cwd = "#{project_dir}/builddir"

  configure_options = [
    "--enable-bind-now",
    "--enable-stack-protector=strong",
    "--build=x86_64-unknown-linux-gnu",
    "--enable-cet",
    "--disable-multi-arch",
    "--disable-werror",
    "--disable-profile",
    "--without-selinux",
    "--disable-crypt",
  ]

  configure(*configure_options, bin: "../configure", env: env, cwd: cwd)

  make "-j #{workers}", env: env, cwd: cwd
  make "install", env: env, cwd: cwd
end
