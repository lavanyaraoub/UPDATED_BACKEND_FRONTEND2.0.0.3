make all

fileName="jamun_v1.0.0"
#$(date +%Y-%m-%d-%T)
mkdir -p "./release"
dir_path="./release/${fileName}"
mkdir -p "${dir_path}"
mkdir "${dir_path}/commands"
mkdir "${dir_path}/configs"

cp ./jamun "${dir_path}"
cp ./ethercatinterface.h "${dir_path}"
cp ./libethercatinterface.so "${dir_path}"
cp ./configs/*.* "${dir_path}/configs"
cp ./commands/*.so "${dir_path}/commands"
du -sh "${dir_path}"
tar -cvzf "./release/${fileName}.tar.gz" "${dir_path}"
rm -rf "${dir_path}"