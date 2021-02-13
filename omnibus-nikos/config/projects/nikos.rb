#
# Copyright 2020 YOUR NAME
#
# All Rights Reserved.
#

name "nikos"
maintainer "Sylvain Baubeau <sylvain.baubeau@datadoghq.com>"
homepage "https://github.com/DataDog/nikos"

# Defaults to C:/nikos on Windows
# and /opt/nikos on all other platforms
install_dir "#{default_root}/#{name}"

build_version Omnibus::BuildVersion.semver
build_iteration 1

# Creates required build directories
dependency "preparation"

if linux?
  dependency 'libdnf'
  dependency 'pacman'
  dependency 'apk-tools'
end


exclude "**/.git"
exclude "**/bundler/git"
