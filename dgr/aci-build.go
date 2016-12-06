package main

import (
	"encoding/json"
	"io/ioutil"
	"os"
	"strconv"

	"github.com/appc/spec/schema"
	"github.com/appc/spec/schema/types"
	"github.com/blablacar/dgr/dgr/common"
	"github.com/n0rad/go-erlog/errs"
	"github.com/n0rad/go-erlog/logs"
)

func (aci *Aci) prepareRktRunArguments(command common.BuilderCommand, builderHash string, stage1Hash string) []string {
	var args []string

	if logs.IsDebugEnabled() {
		args = append(args, "--debug")
	}
	args = append(args, "--set-env="+common.EnvDgrVersion+"="+BuildVersion)
	args = append(args, "--set-env="+common.EnvLogLevel+"="+logs.GetLevel().String())
	args = append(args, "--set-env="+common.EnvAciPath+"="+aci.path)
	args = append(args, "--set-env="+common.EnvAciTarget+"="+aci.target)
	args = append(args, "--set-env="+common.EnvBuilderCommand+"="+string(command))
	args = append(args, "--set-env="+common.EnvCatchOnError+"="+strconv.FormatBool(aci.args.CatchOnError))
	args = append(args, "--set-env="+common.EnvCatchOnStep+"="+strconv.FormatBool(aci.args.CatchOnStep))
	args = append(args, "--net=host")
	args = append(args, "--insecure-options=image")
	args = append(args, "--uuid-file-save="+aci.target+pathBuilderUuid)
	args = append(args, "--interactive")
	if stage1Hash != "" {
		args = append(args, "--stage1-hash="+stage1Hash)
	} else {
		args = append(args, "--stage1-name="+aci.manifest.Builder.Image.String())
	}

	for _, v := range aci.args.SetEnv.Strings() {
		args = append(args, "--set-env="+v)
	}
	args = append(args, builderHash)
	return args
}

func (aci *Aci) RunBuilderCommand(command common.BuilderCommand) error {
	defer aci.giveBackUserRightsToTarget()
	logs.WithF(aci.fields).Info("Building")

	if err := os.MkdirAll(aci.target, 0777); err != nil {
		return errs.WithEF(err, aci.fields, "Cannot create target directory")
	}

	if err := ioutil.WriteFile(aci.target+common.PathManifestYmlTmpl, []byte(aci.manifestTmpl), 0644); err != nil {
		return errs.WithEF(err, aci.fields.WithField("file", aci.target+common.PathManifestYmlTmpl), "Failed to write manifest template")
	}

	stage1Hash, err := aci.prepareStage1aci()
	if err != nil {
		return errs.WithEF(err, aci.fields, "Failed to prepare stage1 image")
	}

	builderHash, err := aci.prepareBuildAci()
	if err != nil {
		return errs.WithEF(err, aci.fields, "Failed to prepare build image")
	}

	logs.WithF(aci.fields).Info("Calling rkt to start build")
	defer aci.cleanupRun(builderHash, stage1Hash)
	if err := Home.Rkt.Run(aci.prepareRktRunArguments(command, builderHash, stage1Hash)); err != nil {
		return errs.WithEF(err, aci.fields, "Builder container return with failed status")
	}

	content, err := common.ExtractManifestContentFromAci(aci.target + pathImageAci)
	if err != nil {
		logs.WithEF(err, aci.fields).Warn("Failed to extract manifest.json")
	}

	if err := ioutil.WriteFile(aci.target+pathManifestJson, content, 0644); err != nil {
		logs.WithEF(err, aci.fields).Warn("Failed to write manifest.json")
	}

	im := &schema.ImageManifest{}
	if err = im.UnmarshalJSON(content); err != nil {
		return errs.WithEF(err, aci.fields.WithField("content", string(content)), "Cannot unmarshall json content")
	}

	fullname := common.ExtractNameVersionFromManifest(im)
	logs.WithField("fullname", *fullname).Info("Finished building aci")
	if err := ioutil.WriteFile(aci.target+pathVersion, []byte(*fullname), 0644); err != nil {
		return errs.WithEF(err, aci.fields, "Failed to write version file in target")
	}

	return nil
}

func (aci *Aci) cleanupRun(builderHash string, stage1Hash string) {
	if !Args.KeepBuilder {
		if _, _, err := Home.Rkt.RmFromFile(aci.target + pathBuilderUuid); err != nil {
			logs.WithEF(err, aci.fields).Warn("Failed to remove build container")
		}
	}

	if err := Home.Rkt.ImageRm(builderHash); err != nil {
		logs.WithEF(err, aci.fields.WithField("hash", builderHash)).Warn("Failed to remove build container image")
	}

	if stage1Hash != "" {
		if err := Home.Rkt.ImageRm(stage1Hash); err != nil {
			logs.WithEF(err, aci.fields.WithField("hash", stage1Hash)).Warn("Failed to remove stage1 container image")
		}
	}
}

func (aci *Aci) Build() error {
	aci.checkDependencies()
	return aci.RunBuilderCommand(common.CommandBuild)
}

func (aci *Aci) CleanAndBuild() error {
	aci.Clean()
	return aci.Build()
}

func (aci *Aci) checkDependencies() {
	aci.checkWg.Add(2)
	if !aci.args.ParallelBuild {
		aci.checkCompatibilityVersions()
		aci.checkLatestVersions()
	} else {
		go aci.checkCompatibilityVersions()
		go aci.checkLatestVersions()
	}
}

func (aci *Aci) prepareStage1aci() (string, error) {
	ImportInternalBuilderIfNeeded(aci.manifest)
	if len(aci.manifest.Builder.Dependencies) == 0 {
		return "", nil
	}

	logs.WithFields(aci.fields).Debug("Preparing stage1")

	if err := os.MkdirAll(aci.target+pathStage1+common.PathRootfs, 0777); err != nil {
		return "", errs.WithEF(err, aci.fields.WithField("path", aci.target+pathBuilder), "Failed to create stage1 aci path")
	}

	Home.Rkt.Fetch(aci.manifest.Builder.Image.String())
	manifestStr, err := Home.Rkt.CatManifest(aci.manifest.Builder.Image.String())
	if err != nil {
		return "", errs.WithEF(err, aci.fields, "Failed to read stage1 image manifest")
	}

	manifest := schema.ImageManifest{}
	if err := json.Unmarshal([]byte(manifestStr), &manifest); err != nil {
		return "", errs.WithEF(err, aci.fields.WithField("content", manifestStr), "Failed to unmarshal stage1 manifest received from rkt")
	}

	manifest.Dependencies = types.Dependencies{}
	dep, err := common.ToAppcDependencies(aci.manifest.Builder.Dependencies)
	if err != nil {
		return "", errs.WithEF(err, aci.fields, "Invalid dependency on stage1 for rkt")
	}
	manifest.Dependencies = append(manifest.Dependencies, dep...)

	stage1Image, err := common.ToAppcDependencies([]common.ACFullname{aci.manifest.Builder.Image})
	if err != nil {
		return "", errs.WithEF(err, aci.fields, "Invalid image on stage1 for rkt")
	}
	manifest.Dependencies = append(manifest.Dependencies, stage1Image...)

	name, err := types.NewACIdentifier(prefixBuilderStage1 + aci.manifest.NameAndVersion.Name())
	if err != nil {
		return "", errs.WithEF(err, aci.fields.WithField("name", prefixBuilderStage1+aci.manifest.NameAndVersion.Name()),
			"aci name is not a valid identifier for rkt")
	}
	manifest.Name = *name

	content, err := json.MarshalIndent(&manifest, "", "  ")
	if err != nil {
		return "", errs.WithEF(err, aci.fields, "Failed to marshal builder's stage1 manifest")
	}

	if err := ioutil.WriteFile(aci.target+pathStage1+common.PathManifest, content, 0644); err != nil {
		return "", errs.WithEF(err, aci.fields.WithField("path", aci.target+pathStage1+common.PathManifest),
			"Failed to write builder's stage1 manifest to file")
	}

	if err := aci.tarAci(aci.target + pathStage1); err != nil {
		return "", err
	}

	logs.WithF(aci.fields.WithField("path", aci.target+pathStage1+pathImageAci)).Info("Importing builder's stage1")
	hash, err := Home.Rkt.FetchInsecure(aci.target + pathStage1 + pathImageAci)
	if err != nil {
		return "", errs.WithEF(err, aci.fields, "fetch of builder's stage1 aci failed")
	}
	return hash, nil
}

func (aci *Aci) prepareBuildAci() (string, error) {
	logs.WithFields(aci.fields).Debug("Preparing builder")

	if err := os.MkdirAll(aci.target+pathBuilder+common.PathRootfs, 0777); err != nil {
		return "", errs.WithEF(err, aci.fields.WithField("path", aci.target+pathBuilder), "Failed to create builder aci path")
	}

	if err := ioutil.WriteFile(aci.target+pathBuilder+common.PathRootfs+"/.keep", []byte(""), 0644); err != nil {
		return "", errs.WithEF(err, aci.fields.WithField("file", aci.target+pathBuilder+common.PathRootfs+"/.keep"), "Failed to write keep file")
	}
	capa, err := types.NewLinuxCapabilitiesRetainSet("all")
	if err != nil {
		return "", errs.WithEF(err, aci.fields, "Failed to create all capability retain Set")
	}
	allIsolator, err := capa.AsIsolator()
	if err != nil {
		return "", errs.WithEF(err, aci.fields, "Failed to prepare all retain set isolator")
	}

	aci.manifest.Aci.App.Isolators = types.Isolators([]types.Isolator{*allIsolator})

	if err := common.WriteAciManifest(aci.manifest, aci.target+pathBuilder+common.PathManifest, common.PrefixBuilder+aci.manifest.NameAndVersion.Name(), BuildVersion); err != nil {
		return "", err
	}
	if err := aci.tarAci(aci.target + pathBuilder); err != nil {
		return "", err
	}

	logs.WithF(aci.fields.WithField("path", aci.target+pathBuilder+pathImageAci)).Info("Importing build to rkt")
	hash, err := Home.Rkt.FetchInsecure(aci.target + pathBuilder + pathImageAci)
	if err != nil {
		return "", errs.WithEF(err, aci.fields, "fetch of builder aci failed")
	}
	return hash, nil
}

func (aci *Aci) EnsureBuilt() error {
	if _, err := os.Stat(aci.target + pathImageAci); os.IsNotExist(err) {
		if err := aci.CleanAndBuild(); err != nil {
			return err
		}
	}
	return nil
}

func (aci *Aci) EnsureSign() error {
	if _, err := os.Stat(aci.target + pathImageAciAsc); os.IsNotExist(err) {
		if err := aci.Sign(); err != nil {
			return err
		}
	}
	return nil
}

func (aci *Aci) EnsureZipSign() error {
	if _, err := os.Stat(aci.target + pathImageGzAciAsc); os.IsNotExist(err) {
		if err := aci.ZipSign(); err != nil {
			return err
		}
	}
	return nil
}

func (aci *Aci) EnsureZip() error {
	if _, err := os.Stat(aci.target + pathImageGzAci); os.IsNotExist(err) {
		if err := aci.EnsureBuilt(); err != nil {
			return err
		}

		if err := aci.zipAci(); err != nil {
			return err
		}
	}
	return nil
}
